package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"
	"unicode/utf8"
)

// ReadOnlyStream takes ownership of a scoped byte connection. Its first frame
// must be an actual exact Ready receipt. Historical receipts cannot construct a
// stream. The observer direction is private and cannot be changed by mutating
// the public Receipt. The connection is closed on every admission failure.
func ReadOnlyStream(ctx context.Context, conn net.Conn, request Request) (*Stream, error) {
	if conn == nil {
		return nil, errors.New("read-only terminal connection required")
	}
	failConn := func(err error) (*Stream, error) { _ = conn.Close(); return nil, err }
	if err := request.validate(); err != nil {
		return failConn(err)
	}
	if !request.Lease.ReadOnly {
		return failConn(errors.New("read-only terminal acquisition required"))
	}
	s := newStream(ctx, conn, nil)
	fail := func(err error) (*Stream, error) { _ = s.Close(); return nil, err }
	if err := conn.SetDeadline(time.Now().Add(ioTimeout)); err != nil {
		return fail(err)
	}
	frame, err := s.ReadFrame()
	if err != nil {
		return fail(err)
	}
	if frame.Kind != Ready {
		return fail(errors.New("read-only terminal requires an actual ready frame"))
	}
	receipt, err := observerReceipt(frame.Data)
	if err != nil {
		return fail(err)
	}
	if receipt != receiptFor(request) {
		return fail(errors.New("read-only terminal receipt differs from original acquisition"))
	}
	s.Receipt = receipt
	s.observer = true
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fail(err)
	}
	return s, nil
}

// Ready is a small fixed schema. Decoder's ordinary last-key-wins and
// case-insensitive field matching must not hide a conflicting authority field.
func observerReceipt(data []byte) (Receipt, error) {
	var receipt Receipt
	invalid := errors.New("invalid read-only terminal receipt")
	if len(data) > 4096 || !utf8.Valid(data) {
		return receipt, invalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return receipt, invalid
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return Receipt{}, invalid
		}
		seen[key] = true
		switch key {
		case "host_id":
			err = d.Decode(&receipt.HostID)
		case "session_id":
			err = d.Decode(&receipt.SessionID)
		case "execution_id":
			err = d.Decode(&receipt.ExecutionID)
		case "generation":
			err = d.Decode(&receipt.Generation)
		case "operation_id":
			err = d.Decode(&receipt.OperationID)
		case "client_id":
			err = d.Decode(&receipt.ClientID)
		case "read_only":
			err = d.Decode(&receipt.ReadOnly)
		default:
			return Receipt{}, invalid
		}
		if err != nil {
			return Receipt{}, invalid
		}
	}
	if len(seen) != 7 {
		return Receipt{}, invalid
	}
	token, err = d.Token()
	if err != nil || token != json.Delim('}') || !errors.Is(d.Decode(new(any)), io.EOF) {
		return Receipt{}, invalid
	}
	return receipt, nil
}
