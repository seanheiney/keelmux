package transport

import (
	"bytes"
	"context"
	"encoding/binary"
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
			state, err := term.GetState(slave.Fd())
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
				current, err := term.GetState(slave.Fd())
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
				if err := pty.Setsize(slave, &pty.Winsize{Rows: 43, Cols: 123}); err != nil {
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
			restored, err := term.GetState(slave.Fd())
			if err != nil || !reflect.DeepEqual(restored, state) {
				t.Fatal("caller terminal state was not restored", err)
			}
			// Its input worker must be joined before the caller resumes reading.
			if _, err := master.Write([]byte("caller resumes\n")); err != nil {
				t.Fatal(err)
			}
			if err := unix.SetNonblock(int(slave.Fd()), true); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 128)
			n, err := readTerminal(ctx, int(slave.Fd()), buffer)
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
