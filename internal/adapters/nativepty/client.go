package nativepty

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
)

// Stream is a connection-bound terminal input lease. Reconnection always opens
// a fresh stream and never replays input. A read-only stream cannot resize.
type Stream struct {
	conn     *net.UnixConn
	mu       sync.Mutex
	ReadOnly bool
	Snapshot []byte
	Offset   uint64
	Detail   string
}

// Output carries observed raw live bytes or an explicit terminal history gap.
type Output struct {
	Data        []byte
	Start, End  uint64
	Gap, Exited bool
	Detail      string
}

func (b *Backend) Open(ctx context.Context, ref domain.RuntimeRef, readOnly bool, rows, cols uint16) (*Stream, error) {
	if !readOnly && !validSize(rows, cols) {
		return nil, errors.New("terminal size exceeds native supported bound")
	}
	r, err := b.receiptForRef(ref)
	if err != nil {
		return nil, err
	}
	c, err := b.connect(ctx, r)
	if err != nil {
		return nil, err
	}
	if err = writeMessage(c, request{Op: "attach", Auth: r.Auth, Ref: ref, ReadOnly: readOnly, Rows: rows, Cols: cols}); err != nil {
		c.Close()
		return nil, err
	}
	var reply response
	if err = readMessage(c, &reply); err != nil {
		c.Close()
		return nil, err
	}
	if reply.Error != "" {
		c.Close()
		return nil, conflict(reply.Error)
	}
	if reply.Kind != "ready" || reply.Ref != ref {
		c.Close()
		return nil, conflict("attach acknowledgement differs from exact runtime")
	}
	_ = c.SetReadDeadline(time.Time{})
	return &Stream{conn: c, ReadOnly: readOnly, Snapshot: reply.Data, Offset: reply.End, Detail: reply.Detail}, nil
}
func (s *Stream) WriteInput(data []byte) error {
	if s.ReadOnly {
		return errors.New("read-only terminal rejects input")
	}
	if len(data) == 0 || len(data) > maxInput {
		return errors.New("terminal input exceeds bound")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeMessage(s.conn, request{Op: "input", Data: data})
}
func (s *Stream) Resize(rows, cols uint16) error {
	if s.ReadOnly {
		return errors.New("read-only terminal rejects resize")
	}
	if !validSize(rows, cols) {
		return errors.New("terminal size exceeds native supported bound")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeMessage(s.conn, request{Op: "resize", Rows: rows, Cols: cols})
}
func (s *Stream) ReadOutput() (Output, error) {
	for {
		var reply response
		if err := readMessage(s.conn, &reply); err != nil {
			return Output{}, err
		}
		if reply.Error != "" {
			return Output{}, errors.New(reply.Error)
		}
		switch reply.Kind {
		case "heartbeat":
			continue
		case "output":
			s.Offset = reply.End
			return Output{Data: reply.Data, Start: reply.Start, End: reply.End}, nil
		case "gap":
			return Output{Gap: true, Start: reply.Start, End: reply.End, Detail: reply.Detail}, nil
		case "exit":
			return Output{Exited: true}, nil
		default:
			return Output{}, errors.New("unexpected native output frame")
		}
	}
}
func (s *Stream) Close() error { return s.conn.Close() }

// Attach enters raw mode before registering the connection with the supervisor;
// kernel peer identity then proves input readiness to the host transport.
func Attach(ctx context.Context, state string, execution, operation domain.ID, instance string, readOnly bool, input, output *os.File) (result error) {
	if input == nil || output == nil {
		return errors.New("native attach requires terminal files")
	}
	fd, err := fileDescriptor(input)
	if err != nil || !term.IsTerminal(uintptr(fd)) {
		return errors.New("native attach requires a terminal")
	}
	b, err := New(state)
	if err != nil {
		return err
	}
	r, err := b.readReceipt(execution)
	if err != nil {
		return err
	}
	if r.Ref.OperationID != operation || r.Ref.Session != instance {
		return conflict("attachment execution instance changed")
	}
	cols, rows, err := term.GetSize(uintptr(fd))
	if err != nil || cols <= 0 || rows <= 0 {
		return errors.New("terminal size unavailable")
	}
	previous, err := term.MakeRaw(uintptr(fd))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, term.Restore(uintptr(fd), previous)) }()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if err = unix.SetNonblock(fd, true); err != nil {
		return err
	}
	defer unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags)
	outputFD, err := fileDescriptor(output)
	if err != nil {
		return err
	}
	outputFlags, err := unix.FcntlInt(uintptr(outputFD), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if err = unix.SetNonblock(outputFD, true); err != nil {
		return err
	}
	defer unix.FcntlInt(uintptr(outputFD), unix.F_SETFL, outputFlags)

	stream, err := b.Open(ctx, r.Ref, readOnly, uint16(rows), uint16(cols))
	if err != nil {
		return err
	}
	defer stream.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopCancel := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stopCancel()
	if err = writePTY(output, stream.Snapshot); err != nil {
		return err
	}
	// Reset only terminal modes we manage, after detach. This does not write
	// input, terminal queries, clipboard requests or OSC effects into the child.
	defer func() {
		_ = writePTY(output, []byte("\x1b[0m\x1b[?25h\x1b[?1l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1004l\x1b[?1006l\x1b[?2004l\x1b[?1049l"))
	}()
	var workers sync.WaitGroup
	defer workers.Wait()
	detached := make(chan struct{}, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer cancel()
		prefix := false
		buffer := make([]byte, maxInput)
		for {
			n, err := readTerminal(ctx, fd, buffer)
			if err != nil {
				return
			}
			out := make([]byte, 0, n+1)
			for _, key := range buffer[:n] {
				if prefix {
					prefix = false
					if key == 'd' {
						if len(out) > 0 {
							_ = stream.WriteInput(out)
						}
						select {
						case detached <- struct{}{}:
						default:
						}
						return
					}
					if !readOnly {
						out = append(out, 7)
						if key != 7 {
							out = append(out, key)
						}
					}
					continue
				}
				if key == 7 {
					prefix = true
					continue
				}
				if !readOnly {
					out = append(out, key)
				}
			}
			if len(out) > 0 {
				if err = stream.WriteInput(out); err != nil {
					return
				}
			}
		}
	}()
	if !readOnly {
		resizes := make(chan os.Signal, 1)
		signal.Notify(resizes, syscall.SIGWINCH)
		defer signal.Stop(resizes)
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case <-resizes:
					cols, rows, err := term.GetSize(uintptr(fd))
					if err == nil && cols > 0 && rows > 0 {
						if err = stream.Resize(uint16(rows), uint16(cols)); err != nil {
							cancel()
							return
						}
					}
				}
			}
		}()
	}
	for {
		frame, err := stream.ReadOutput()
		if err != nil {
			cancel()
			select {
			case <-detached:
				return nil
			default:
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if frame.Exited {
			cancel()
			return nil
		}
		if frame.Gap {
			cancel()
			return fmt.Errorf("native terminal history gap: %s", frame.Detail)
		}
		if err = writePTY(output, frame.Data); err != nil {
			cancel()
			return err
		}
	}
}
