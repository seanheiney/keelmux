package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestDriveNativeKeysAndTerminalRestoration(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "writer", true: "observer"}[readOnly], func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			slaveFD, flags, err := descriptor(slave)
			if err != nil {
				t.Fatal(err)
			}
			state, err := term.GetState(uintptr(slaveFD))
			if err != nil {
				t.Fatal(err)
			}
			output, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			a, b := net.Pipe()
			client, server := newStream(ctx, a, nil), newStream(ctx, b, nil)
			defer client.Close()
			defer server.Close()
			client.Receipt.ReadOnly = readOnly
			driven := make(chan error, 1)
			go func() { driven <- Drive(ctx, client, slave, output) }()
			deadline := time.Now().Add(time.Second)
			for {
				current, err := term.GetState(uintptr(slaveFD))
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(current, state) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("Drive did not enter raw mode")
				}
				time.Sleep(time.Millisecond)
			}
			if err := server.WriteFrame(Frame{Output, []byte("Native 🛠\r\n")}); err != nil {
				t.Fatal(err)
			}
			// Wait for the visible frame before simulating a user's detach.
			// A transport write alone does not mean the display has drained it;
			// racing local cancellation tests a different, unclaimed guarantee.
			for {
				visible, err := os.ReadFile(output.Name())
				if err != nil {
					t.Fatal(err)
				}
				if string(visible) == "Native 🛠\r\n" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("native output was not displayed before input")
				}
				time.Sleep(time.Millisecond)
			}
			keys := []byte("Native 🛠\x03\x1b\x07\x07")
			if !readOnly {
				if _, err := master.Write(keys); err != nil {
					t.Fatal(err)
				}
				var got []byte
				for len(got) < len(keys) {
					f, err := server.ReadFrame()
					if err != nil || f.Kind != Input {
						t.Fatalf("native input: %+v %v", f, err)
					}
					got = append(got, f.Data...)
				}
				if !bytes.Equal(got, keys) {
					t.Fatalf("native keys rewritten: %q", got)
				}
				if err := unix.IoctlSetWinsize(slaveFD, unix.TIOCSWINSZ, &unix.Winsize{Row: 43, Col: 123}); err != nil {
					t.Fatal(err)
				}
				// SIGWINCH targets this test process, not the user's terminal.
				if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
					t.Fatal(err)
				}
				f, err := server.ReadFrame()
				if err != nil || f.Kind != Resize || len(f.Data) != 4 || binary.BigEndian.Uint16(f.Data) != 43 || binary.BigEndian.Uint16(f.Data[2:]) != 123 {
					t.Fatalf("native resize: %+v %v", f, err)
				}
				if err := server.WriteFrame(Frame{Close, nil}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := master.Write(append(keys, 7, 'd')); err != nil {
					t.Fatal(err)
				}
				if f, err := server.ReadFrame(); err == nil {
					t.Fatalf("observer forwarded input: %+v", f)
				}
			}
			select {
			case err := <-driven:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("Drive did not return")
			}
			restored, err := term.GetState(uintptr(slaveFD))
			if err != nil || !reflect.DeepEqual(restored, state) {
				t.Fatal("caller terminal state was not restored", err)
			}
			_, restoredFlags, err := descriptor(slave)
			if err != nil || restoredFlags != flags {
				t.Fatalf("caller input flags changed: %x -> %x: %v", flags, restoredFlags, err)
			}
			// Its input worker must be joined before the caller resumes reading.
			if _, err := master.Write([]byte("caller resumes\n")); err != nil {
				t.Fatal(err)
			}
			if err := unix.SetNonblock(slaveFD, true); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 128)
			n, err := readTerminal(ctx, slaveFD, buffer)
			if err != nil || !bytes.Equal(buffer[:n], []byte("caller resumes\n")) {
				t.Fatalf("Drive reader consumed caller input: %q %v", buffer[:n], err)
			}
			data, err := os.ReadFile(output.Name())
			if err != nil || string(data) != "Native 🛠\r\n" {
				t.Fatal("native output changed", string(data), err)
			}
		})
	}
}

func TestDriveCancelWithBlockedDisplay(t *testing.T) {
	for _, nonblock := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking caller", true: "nonblocking caller"}[nonblock], func(t *testing.T) {
			master, input, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer input.Close()
			reader, output, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer output.Close()
			inputFD, _, err := descriptor(input)
			if err != nil {
				t.Fatal(err)
			}
			outputFD, _, err := descriptor(output)
			if err != nil {
				t.Fatal(err)
			}
			fillDescriptor(t, outputFD)
			if err := unix.SetNonblock(inputFD, nonblock); err != nil {
				t.Fatal(err)
			}
			if err := unix.SetNonblock(outputFD, nonblock); err != nil {
				t.Fatal(err)
			}
			_, inputFlags, _ := descriptor(input)
			_, outputFlags, _ := descriptor(output)
			state, err := term.GetState(uintptr(inputFD))
			if err != nil {
				t.Fatal(err)
			}
			// Drive cancellation must also close a stream created under a
			// longer-lived context, rather than depend on its caller doing so.
			a, b := net.Pipe()
			client, server := newStream(context.Background(), a, nil), newStream(context.Background(), b, nil)
			defer client.Close()
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ended := make(chan error, 1)
			go func() { ended <- Drive(ctx, client, input, output) }()
			if err := server.WriteFrame(Frame{Output, make([]byte, MaxFrame)}); err != nil {
				t.Fatal(err)
			}
			cancel()
			select {
			case err := <-ended:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("unexpected cancellation result", err)
				}
			case <-time.After(500 * time.Millisecond):
				// Closing only the owned reader unblocks the old regression
				// without closing the caller's descriptor inside Drive.
				_ = reader.Close()
				select {
				case <-ended:
				case <-time.After(time.Second):
					t.Fatal("fixture cleanup stalled")
				}
				t.Fatal("Drive blocked in display output after cancellation")
			}
			restored, err := term.GetState(uintptr(inputFD))
			if err != nil || !reflect.DeepEqual(restored, state) {
				t.Fatal("raw terminal state was not restored", err)
			}
			_, gotInput, err := descriptor(input)
			if err != nil || gotInput != inputFlags {
				t.Fatalf("input flags changed: %x -> %x: %v", inputFlags, gotInput, err)
			}
			_, gotOutput, err := descriptor(output)
			if err != nil || gotOutput != outputFlags {
				t.Fatalf("output flags changed: %x -> %x: %v", outputFlags, gotOutput, err)
			}
			if err := unix.SetNonblock(inputFD, true); err != nil {
				t.Fatal(err)
			}
			if _, err := master.Write([]byte("caller resumes\n")); err != nil {
				t.Fatal(err)
			}
			probe, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			data := make([]byte, 64)
			n, err := readTerminal(probe, inputFD, data)
			if err != nil || string(data[:n]) != "caller resumes\n" {
				t.Fatalf("input reader was not joined: %q %v", data[:n], err)
			}
		})
	}
}

func fillDescriptor(t *testing.T, fd int) {
	t.Helper()
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 4096)
	deadline := time.Now().Add(time.Second)
	for {
		_, err := unix.Write(fd, data)
		if err == unix.EAGAIN {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("owned descriptor did not reach backpressure")
		}
	}
}

func TestTerminalIOCancellation(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	slaveFD, _, err := descriptor(slave)
	if err != nil {
		t.Fatal(err)
	}
	state, err := term.MakeRaw(uintptr(slaveFD))
	if err != nil {
		t.Fatal(err)
	}
	defer term.Restore(uintptr(slaveFD), state)
	fd, _, err := descriptor(master)
	if err != nil {
		t.Fatal(err)
	}
	fillDescriptor(t, fd)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := writeTerminal(ctx, fd, make([]byte, MaxFrame)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("PTY backpressure ignored deadline", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("PTY write cancellation was not bounded")
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := readTerminal(ctx, fd, make([]byte, 8)); !errors.Is(err, context.Canceled) {
		t.Fatal("PTY read ignored cancellation", err)
	}
}

func TestDriveSharedTerminalFlagsAndIdleCancellation(t *testing.T) {
	master, input, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer input.Close()
	inputFD, _, err := descriptor(input)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Dup(inputFD)
	if err != nil {
		t.Fatal(err)
	}
	output := os.NewFile(uintptr(fd), "shared output tty")
	defer output.Close()
	// stdin and stdout can share one open file description. Capture both
	// original flags before either descriptor changes that shared state.
	if err := unix.SetNonblock(inputFD, true); err != nil {
		t.Fatal(err)
	}
	_, inputFlags, _ := descriptor(input)
	_, outputFlags, _ := descriptor(output)
	state, err := term.GetState(uintptr(inputFD))
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	client, server := newStream(context.Background(), a, nil), newStream(context.Background(), b, nil)
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan error, 1)
	go func() { ended <- Drive(ctx, client, input, output) }()
	if err := server.WriteFrame(Frame{Output, []byte("owned shared tty\r\n")}); err != nil {
		t.Fatal(err)
	}
	masterFD, _, err := descriptor(master)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(masterFD, true); err != nil {
		t.Fatal(err)
	}
	probe, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	buffer := make([]byte, 64)
	n, err := readTerminal(probe, masterFD, buffer)
	if err != nil || string(buffer[:n]) != "owned shared tty\r\n" {
		t.Fatalf("shared tty output changed: %q %v", buffer[:n], err)
	}
	cancel() // No stream frame follows; the long-lived stream must close.
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("unexpected idle cancellation", err)
		}
	case <-time.After(500 * time.Millisecond):
		_ = server.Close()
		<-ended
		t.Fatal("idle Drive ignored its own cancellation context")
	}
	_, inputAfter, _ := descriptor(input)
	_, outputAfter, _ := descriptor(output)
	// After tty output Darwin reports an extra status bit that F_SETFL cannot
	// clear. Compare caller-controlled flags rather than kernel bookkeeping.
	const mutableFlags = unix.O_NONBLOCK | unix.O_APPEND | unix.O_ASYNC
	if inputFlags&mutableFlags != inputAfter&mutableFlags || outputFlags&mutableFlags != outputAfter&mutableFlags {
		t.Fatalf("shared terminal flags changed: in %x/%x out %x/%x", inputFlags, inputAfter, outputFlags, outputAfter)
	}
	restored, err := term.GetState(uintptr(inputFD))
	if err != nil || !reflect.DeepEqual(restored, state) {
		t.Fatal("shared terminal state not restored", err)
	}
}
