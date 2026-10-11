package nativepty

import (
	"bytes"
	"encoding/json"
	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type stdioRecord struct {
	Event ps.Event
	Raw   []byte
}
type stdioStep struct {
	Name, ID                   string
	Attempted, Sent, Completed bool
}
type stdioState struct {
	Version                      int
	Channel                      ps.Channel
	Directories                  []stdioDirectory
	Bootstrap                    ps.BootstrapState
	Continuity                   ps.Continuity
	Cursor, Ack                  ps.Cursor
	Records                      []stdioRecord
	Steps                        []stdioStep
	Pending                      bool
	Snapshot                     ps.Snapshot
	Batches                      []ps.ImportAck
	StopOperation                domain.ID
	TermAttempted, KillAttempted bool
	StderrBytes                  uint64
	StderrTail                   []byte
	StderrTruncated              bool
	NoticeRevision               uint64
}

func (b *Backend) stdioPath(id domain.ID) string {
	return filepath.Join(b.state, executionKey(id)+".stdio.json")
}
func (b *Backend) saveStdio(state stdioState) error {
	if privateDirectory(b.state) != nil {
		return ps.ErrStorage
	}
	data, err := json.Marshal(state)
	if err != nil || len(data) > ps.MaxEventBytes {
		return ps.ErrStorage
	}
	if err = atomicWrite(b.stdioPath(state.Channel.Spec.Binding.ExecutionID), data); err != nil {
		return ps.ErrStorage
	}
	return nil
}
func (b *Backend) readStdio(r receipt) (stdioState, error) {
	var state stdioState
	f, err := openPrivate(b.stdioPath(r.Ref.ExecutionID), unix.O_RDONLY)
	if err != nil {
		return state, ps.ErrStorage
	}
	defer f.Close()
	const max = ps.MaxEventBytes
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || len(raw) > max {
		return state, ps.ErrStorage
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&state) != nil {
		return state, ps.ErrStorage
	}
	canonical, _ := json.Marshal(state)
	if !bytes.Equal(raw, canonical) || state.Version != ps.Version || r.Stdio == nil || state.Channel.Spec != r.Stdio.Spec || state.Channel.Ref.PrimaryIncarnation != r.Stdio.Ref.PrimaryIncarnation || state.Channel.Ref.ChannelIncarnation != r.Stdio.Ref.ChannelIncarnation || ps.ValidateCursor(state.Cursor) != nil || ps.ValidateCursor(state.Ack) != nil || state.Ack.Seq > state.Cursor.Seq {
		return state, ps.ErrStorage
	}
	// Receipt may still be attempted before running child identity is committed.
	if state.Channel.Ref.Runtime != r.Ref {
		return state, ps.ErrStorage
	}
	cur := state.Ack
	for _, record := range state.Records {
		if len(record.Raw) != int(record.Event.FrameBytes) || stdioFrameHash(record.Raw) != record.Event.FrameSHA256 {
			return state, ps.ErrStorage
		}
		if record.Event.Cursor.Seq <= state.Ack.Seq {
			if !ps.EventBlocks(record.Event) {
				return state, ps.ErrStorage
			}
			continue
		}
		next, e := ps.AdvanceCursor(cur, record.Event)
		if e != nil || next != record.Event.Cursor {
			return state, ps.ErrStorage
		}
		cur = next
	}
	if cur != state.Cursor {
		return state, ps.ErrStorage
	}
	return state, nil
}

type stdioDisk struct {
	Device, Inode uint64
	SHA256        string
}

func (b *Backend) stdioDisk(id domain.ID) (stdioDisk, error) {
	if privateDirectory(b.state) != nil {
		return stdioDisk{}, ps.ErrStorage
	}
	f, err := openPrivate(b.stdioPath(id), unix.O_RDONLY)
	if err != nil {
		return stdioDisk{}, ps.ErrStorage
	}
	defer f.Close()
	i, err := f.Stat()
	if err != nil {
		return stdioDisk{}, ps.ErrStorage
	}
	st, ok := i.Sys().(*syscall.Stat_t)
	if !ok {
		return stdioDisk{}, ps.ErrStorage
	}
	raw, err := io.ReadAll(io.LimitReader(f, ps.MaxEventBytes+1))
	if err != nil || len(raw) > ps.MaxEventBytes {
		return stdioDisk{}, ps.ErrStorage
	}
	pathInfo, err := os.Lstat(b.stdioPath(id))
	if err != nil || !os.SameFile(i, pathInfo) {
		return stdioDisk{}, ps.ErrStorage
	}
	return stdioDisk{uint64(st.Dev), st.Ino, stdioFrameHash(raw)}, nil
}
