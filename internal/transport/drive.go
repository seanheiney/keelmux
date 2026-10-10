package transport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

// Drive preserves the native writer's bytes. It restores terminal state and
// joins its input reader before returning, so the calling TUI can resume safely.
// A read-only viewer consumes no pane input; Ctrl-G d closes that viewer locally.
func Drive(ctx context.Context, stream *Stream, input, output *os.File) (result error) {
	if stream == nil || input == nil || output == nil {
		return errors.New("native drive requires a terminal and an opened private stream")
	}
	inputFD, inputFlags, err := descriptor(input)
	if err != nil || !term.IsTerminal(uintptr(inputFD)) {
		return errors.New("native drive requires a terminal and an opened private stream")
	}
	outputFD, outputFlags, err := descriptor(output)
	if err != nil {
		return err
	}
	state, err := term.MakeRaw(uintptr(inputFD))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(ctx, func() { _ = stream.Close() })
	var workers sync.WaitGroup
	defer func() {
		cancel()
		stopCancel()
		_ = stream.Close()
		workers.Wait()
		result = errors.Join(result, term.Restore(uintptr(inputFD), state), setFlags(inputFD, inputFlags), setFlags(outputFD, outputFlags))
		runtime.KeepAlive(input)
		runtime.KeepAlive(output)
	}()
	if err := setFlags(inputFD, inputFlags|unix.O_NONBLOCK); err != nil {
		return err
	}
	if err := setFlags(outputFD, outputFlags|unix.O_NONBLOCK); err != nil {
		return err
	}
	inputError := make(chan error, 1)
	finishInput := func(err error) {
		select {
		case inputError <- err:
		default:
		}
		cancel()
		_ = stream.Close()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		buffer := make([]byte, MaxFrame)
		prefix := false
		for {
			if err := pollTerminal(ctx, inputFD, unix.POLLIN); err != nil {
				return
			}
			n, err := unix.Read(inputFD, buffer)
			if err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			if err != nil || n == 0 {
				finishInput(err)
				return
			}
			if stream.Receipt.ReadOnly {
				for _, key := range buffer[:n] {
					if prefix && key == 'd' {
						finishInput(nil)
						return
					}
					prefix = key == 7
				}
				continue
			}
			if err := stream.WriteInput(buffer[:n]); err != nil {
				finishInput(err)
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
					cols, rows, err := term.GetSize(uintptr(inputFD))
					if err == nil && rows > 0 && cols > 0 && rows <= 4096 && cols <= 4096 {
						if err := stream.Resize(uint16(rows), uint16(cols)); err != nil {
							finishInput(err)
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
			if err := writeTerminal(ctx, outputFD, frame.Data); err != nil {
				select {
				case inputErr := <-inputError:
					return inputErr
				default:
				}
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
