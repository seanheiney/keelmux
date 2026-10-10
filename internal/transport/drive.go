package transport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

// Drive preserves the native writer's bytes. It restores terminal state and
// joins its input reader before returning, so the calling TUI can resume safely.
// A read-only viewer consumes no pane input; Ctrl-G d closes that viewer locally.
func Drive(ctx context.Context, stream *Stream, input, output *os.File) error {
	if stream == nil || input == nil || output == nil || !term.IsTerminal(input.Fd()) {
		return errors.New("native drive requires a terminal and an opened private stream")
	}
	state, err := term.MakeRaw(input.Fd())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); _ = stream.Close(); workers.Wait(); _ = term.Restore(input.Fd(), state) }()
	inputError := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		buffer := make([]byte, MaxFrame)
		prefix := false
		for {
			if err := pollTerminal(ctx, int(input.Fd()), unix.POLLIN); err != nil {
				return
			}
			n, err := unix.Read(int(input.Fd()), buffer)
			if err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			if err != nil || n == 0 {
				select {
				case inputError <- err:
				default:
				}
				_ = stream.Close()
				return
			}
			if stream.Receipt.ReadOnly {
				for _, key := range buffer[:n] {
					if prefix && key == 'd' {
						select {
						case inputError <- nil:
						default:
						}
						_ = stream.Close()
						return
					}
					prefix = key == 7
				}
				continue
			}
			if err := stream.WriteInput(buffer[:n]); err != nil {
				select {
				case inputError <- err:
				default:
				}
				_ = stream.Close()
				return
			}
		}
	}()
	if !stream.Receipt.ReadOnly {
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
					cols, rows, err := term.GetSize(input.Fd())
					if err == nil && rows > 0 && cols > 0 && rows <= 4096 && cols <= 4096 {
						if err := stream.Resize(uint16(rows), uint16(cols)); err != nil {
							_ = stream.Close()
							return
						}
					}
				}
			}
		}()
	}
	for {
		frame, err := stream.ReadFrame()
		if err != nil {
			select {
			case inputErr := <-inputError:
				return inputErr
			default:
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		switch frame.Kind {
		case Output:
			if err := writeAll(output, frame.Data); err != nil {
				return err
			}
		case Close:
			return nil
		case Failure:
			return fmt.Errorf("%s", frame.Data)
		default:
			return errors.New("host sent a forbidden terminal frame direction")
		}
	}
}
