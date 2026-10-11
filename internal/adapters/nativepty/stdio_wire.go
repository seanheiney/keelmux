package nativepty

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
	"io"
	"net"
	"time"
	"unicode/utf8"
)

// This union is private. No field accepts native JSON, argv, or callback answers.
type stdioRequest struct {
	Op         string            `json:"op"`
	Auth       string            `json:"auth,omitempty"`
	Ref        domain.RuntimeRef `json:"ref"`
	Version    int               `json:"version,omitempty"`
	Channel    *ps.ChannelRef    `json:"channel,omitempty"`
	Cursor     *ps.Cursor        `json:"cursor,omitempty"`
	Ack        *ps.ImportAck     `json:"ack,omitempty"`
	Quiescence *ps.Quiescence    `json:"quiescence,omitempty"`
	Stop       *ps.StopRequest   `json:"stop,omitempty"`
	GuardID    string            `json:"guard_id,omitempty"`
	// Generic inspect's existing zero terminal dimensions remain readable.
	Rows, Cols uint16
}
type stdioResponse struct {
	Error       string            `json:"error,omitempty"`
	Observation *ps.Observation   `json:"observation,omitempty"`
	GuardID     string            `json:"guard_id,omitempty"`
	Ref         domain.RuntimeRef `json:"ref,omitempty"`
	Alive       bool              `json:"alive,omitempty"`
	Detail      string            `json:"detail,omitempty"`
}

// The connection already carries its operation/context deadline. In particular,
// writing a guard release must never extend a shorter caller deadline.
func writeStdioMessage(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) == 0 || len(data) > maxMessage {
		return ps.ErrInvalid
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
	if err = writeAll(w, prefix[:]); err != nil {
		return err
	}
	return writeAll(w, data)
}

func strictJSONKeys(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ps.ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return ps.ErrInvalid
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || keys[name] {
				return ps.ErrInvalid
			}
			keys[name] = true
			if err = strictJSONKeys(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err = strictJSONKeys(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ps.ErrInvalid
	}
	_, err = d.Token()
	return err
}
func readStdioMessage(r io.Reader, v any) error {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n == 0 || n > maxMessage {
		return ps.ErrInvalid
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return ps.ErrInvalid
	}
	keys := json.NewDecoder(bytes.NewReader(data))
	keys.UseNumber()
	if strictJSONKeys(keys, 0) != nil {
		return ps.ErrInvalid
	}
	if _, err := keys.Token(); err != io.EOF {
		return ps.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ps.ErrInvalid
	}
	if d.Decode(new(any)) != io.EOF {
		return ps.ErrInvalid
	}
	canonical, err := json.Marshal(v)
	if err != nil || !bytes.Equal(canonical, data) {
		return ps.ErrInvalid
	}
	return nil
}
func (b *Backend) stdioConnect(ctx context.Context, ref ps.ChannelRef) (*net.UnixConn, receipt, error) {
	r, err := b.stdioReceipt(ref)
	if err != nil {
		return nil, r, err
	}
	c, err := b.connect(ctx, r)
	if err != nil {
		return nil, r, err
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetDeadline(deadline)
	return c, r, nil
}
func (b *Backend) stdioCommand(ctx context.Context, ref ps.ChannelRef, req stdioRequest) (stdioResponse, error) {
	c, r, err := b.stdioConnect(ctx, ref)
	if err != nil {
		return stdioResponse{}, err
	}
	defer c.Close()
	req.Version = ps.Version
	req.Auth = r.Auth
	req.Ref = r.Ref
	req.Channel = &ref
	if err = writeStdioMessage(c, req); err != nil {
		return stdioResponse{}, ps.ErrUncertain
	}
	var reply stdioResponse
	if err = readStdioMessage(c, &reply); err != nil {
		return reply, ps.ErrUncertain
	}
	return reply, stdioError(reply.Error)
}
func (b *Backend) BootstrapStdioOnce(ctx context.Context, ref ps.ChannelRef) (ps.Observation, error) {
	reply, err := b.stdioCommand(ctx, ref, stdioRequest{Op: "stdio_bootstrap"})
	if err != nil && reply.Observation == nil {
		return ps.Observation{}, err
	}
	o, validation := b.stdioObservation(ref, reply)
	if validation != nil {
		return o, validation
	}
	return o, err
}
func (b *Backend) ObserveStdio(ctx context.Context, ref ps.ChannelRef, cursor ps.Cursor) (ps.Observation, error) {
	r, e := b.stdioReceipt(ref)
	if e != nil {
		return ps.Observation{}, e
	}
	if r.Stage == "stopped" || r.Stage == "exited" {
		state, err := b.readStdio(r)
		if err != nil {
			return ps.Observation{}, err
		}
		return terminalStdioObservation(state, cursor)
	}
	reply, err := b.stdioCommand(ctx, ref, stdioRequest{Op: "stdio_observe", Cursor: &cursor})
	if err != nil && reply.Observation == nil {
		return ps.Observation{}, err
	}
	o, validation := b.stdioObservation(ref, reply)
	if validation != nil {
		return o, validation
	}
	return o, err
}
func (b *Backend) RetainedStdio(ctx context.Context, ref ps.ChannelRef, cursor ps.Cursor) (ps.Observation, error) {
	r, e := b.stdioReceipt(ref)
	if e != nil {
		return ps.Observation{}, e
	}
	if r.Stage == "stopped" || r.Stage == "exited" {
		state, err := b.readStdio(r)
		if err != nil {
			return ps.Observation{}, err
		}
		return terminalStdioObservation(state, cursor)
	}
	reply, err := b.stdioCommand(ctx, ref, stdioRequest{Op: "stdio_retained", Cursor: &cursor})
	if err != nil && reply.Observation == nil {
		return ps.Observation{}, err
	}
	o, validation := b.stdioObservation(ref, reply)
	if validation != nil {
		return o, validation
	}
	return o, err
}
func (b *Backend) stdioObservation(ref ps.ChannelRef, reply stdioResponse) (ps.Observation, error) {
	if reply.Observation == nil {
		return ps.Observation{}, ps.ErrUnavailable
	}
	r, err := b.stdioReceipt(ref)
	if err != nil {
		return ps.Observation{}, err
	}
	if ps.ValidateObservation(*r.Stdio, *reply.Observation) != nil {
		return ps.Observation{}, ps.ErrConflict
	}
	return *reply.Observation, nil
}
func (b *Backend) AckStdioImported(ctx context.Context, ref ps.ChannelRef, ack ps.ImportAck) error {
	r, e := b.stdioReceipt(ref)
	if e != nil {
		return e
	}
	if r.Stage == "stopped" || r.Stage == "exited" {
		state, err := b.readStdio(r)
		if err != nil {
			return err
		}
		o, err := terminalStdioObservation(state, ack.From)
		if err != nil {
			return err
		}
		if o.Through != ack.Through || o.BatchSHA256 != ack.BatchSHA256 {
			return ps.ErrConflict
		}
		return nil
	}
	_, err := b.stdioCommand(ctx, ref, stdioRequest{Op: "stdio_ack", Ack: &ack})
	return err
}

// Confirmed terminal receipts expose retained evidence only. No native query,
// helper spawn, ledger repair or writer effect is possible on this path.
func terminalStdioObservation(state stdioState, from ps.Cursor) (ps.Observation, error) {
	if ps.ValidateChannel(state.Channel) != nil || ps.ValidateCursor(from) != nil || from.ChannelIncarnation != state.Channel.Ref.ChannelIncarnation || from.Seq < state.Ack.Seq || from.Seq > state.Cursor.Seq {
		return ps.Observation{}, ps.ErrConflict
	}
	known := from == state.Ack || from == state.Cursor
	for _, record := range state.Records {
		if record.Event.Cursor == from {
			known = true
		}
	}
	if !known {
		return ps.Observation{}, ps.ErrConflict
	}
	snapshot := ps.Snapshot{Channel: state.Channel.Ref, Binding: state.Channel.Spec.Binding, Cursor: state.Cursor, Bootstrap: state.Bootstrap, Continuity: state.Continuity, Pending: state.Pending, StderrTruncated: state.StderrTruncated}
	snapshot.SnapshotSHA256 = ps.SnapshotDigest(snapshot)
	o := ps.Observation{Snapshot: snapshot, From: from, Through: from}
	size := uint64(0)
	for _, record := range state.Records {
		if record.Event.Cursor.Seq <= from.Seq {
			continue
		}
		if len(o.Events) >= ps.MaxExchangeFrames || size+uint64(record.Event.FrameBytes) > ps.MaxExchangeBytes {
			o.More = true
			break
		}
		o.Events = append(o.Events, record.Event)
		o.Through = record.Event.Cursor
		size += uint64(record.Event.FrameBytes)
	}
	o.BatchSHA256 = ps.BatchDigest(o)
	if ps.ValidateObservation(state.Channel, o) != nil {
		return o, ps.ErrStorage
	}
	return o, nil
}
func (b *Backend) StopStdio(ctx context.Context, ref ps.ChannelRef, stop ps.StopRequest) error {
	r, err := b.stdioReceipt(ref)
	if err != nil {
		return err
	}
	return b.stopStdioFromReceipt(ctx, ref, stop, r)
}
func (b *Backend) stopStdioFromReceipt(ctx context.Context, ref ps.ChannelRef, stop ps.StopRequest, r receipt) error {
	if r.Stage == "stopped" {
		return b.terminalStdioStop(r, ref, stop)
	}
	reply, err := b.stdioCommand(ctx, ref, stdioRequest{Op: "stdio_stop", Stop: &stop})
	if reply.Error == "" && (errors.Is(err, ErrUnavailable) || errors.Is(err, ps.ErrUncertain)) {
		// The helper may finish while this caller holds an older stopping
		// receipt, or exit before its final IPC acknowledgment is delivered.
		// Reconcile once from exact durable evidence; never resend the request.
		// An explicit failure remains authoritative even if a failed write
		// made a terminal file visible before its directory sync failed.
		terminal, readErr := b.stdioReceipt(ref)
		if readErr == nil && terminal.Stage == "stopped" {
			return b.terminalStdioStop(terminal, ref, stop)
		}
	}
	return err
}
func (b *Backend) terminalStdioStop(r receipt, ref ps.ChannelRef, stop ps.StopRequest) error {
	if r.Stage != "stopped" || r.Stdio == nil || r.Stdio.Ref != ref {
		return ps.ErrConflict
	}
	state, err := b.readStdio(r)
	if err != nil {
		return err
	}
	if state.StopOperation != stop.OperationID || !state.TermAttempted {
		return ps.ErrConflict
	}
	return nil
}
func (b *Backend) WithStdioQuiescence(ctx context.Context, ref ps.ChannelRef, q ps.Quiescence, commit func(context.Context) error) error {
	if commit == nil {
		return ps.ErrInvalid
	}
	c, r, err := b.stdioConnect(ctx, ref)
	if err != nil {
		return err
	}
	defer c.Close()
	// A guard has a fixed 2s ceiling, also exposed to the local store callback.
	inner, cancel := context.WithTimeout(ctx, ioTimeout)
	defer cancel()
	d, _ := inner.Deadline()
	_ = c.SetDeadline(d)
	req := stdioRequest{Op: "stdio_guard", Auth: r.Auth, Ref: r.Ref, Version: ps.Version, Channel: &ref, Quiescence: &q}
	if writeStdioMessage(c, req) != nil {
		return ps.ErrUncertain
	}
	var reply stdioResponse
	if readStdioMessage(c, &reply) != nil {
		return ps.ErrUncertain
	}
	if err = stdioError(reply.Error); err != nil {
		return err
	}
	if len(reply.GuardID) != 64 {
		return ps.ErrUncertain
	}
	callbackErr := commit(inner)
	release := stdioRequest{Op: "stdio_release", Auth: r.Auth, Ref: r.Ref, Version: ps.Version, Channel: &ref, GuardID: reply.GuardID}
	if inner.Err() != nil || writeStdioMessage(c, release) != nil {
		return ps.ErrUncertain
	}
	reply = stdioResponse{}
	if readStdioMessage(c, &reply) != nil || reply.GuardID != release.GuardID || reply.Error != "" {
		return ps.ErrUncertain
	}
	return callbackErr
}
