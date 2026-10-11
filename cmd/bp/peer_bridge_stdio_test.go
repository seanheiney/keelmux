package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Exec inherits blocking descriptors, unlike os.Pipe/net.Pipe objects created
// inside a test. Keep the parent endpoints open to prove cancellation itself
// interrupts a quiet read and a write blocked on a non-reading SSH peer.
func TestPeerBridgeInheritedStdioInterruptsReadAndStalledWrite(t *testing.T) {
	for _, mode := range []string{"read", "write"} {
		t.Run(mode, func(t *testing.T) {
			in, parentWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			parentReader, out, err := os.Pipe()
			if err != nil {
				_ = in.Close()
				_ = parentWriter.Close()
				t.Fatal(err)
			}
			defer parentWriter.Close()
			defer parentReader.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPeerBridgeStdioSubprocess$")
			cmd.Env = append(os.Environ(), "KEELMUX_TEST_BRIDGE_STDIO="+mode)
			cmd.Stdin, cmd.Stdout = in, out
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			if err = cmd.Start(); err != nil {
				_ = in.Close()
				_ = out.Close()
				t.Fatal(err)
			}
			_ = in.Close()
			_ = out.Close()
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			if mode == "read" {
				if _, err = parentWriter.Write([]byte{'A'}); err != nil {
					cancel()
					<-waited
					t.Fatal(err)
				}
			}
			if err = parentReader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				cancel()
				<-waited
				t.Fatal(err)
			}
			var ready [1]byte
			_, readyErr := io.ReadFull(parentReader, ready[:])
			if readyErr != nil || ready[0] != 'R' {
				cancel()
				<-waited
				t.Fatal("inherited subprocess did not reach live IO", readyErr, diagnostic.String())
			}
			// For write mode, stop consuming after this first live byte. For read
			// mode, never close the retained parentWriter to manufacture EOF.
			if err = <-waited; err != nil {
				t.Fatal("owned inherited IO did not interrupt and exit", err, diagnostic.String())
			}
			if ctx.Err() != nil {
				t.Fatal("owned process required timeout cleanup")
			}
			if mode == "read" {
				if err = parentReader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if n, err := parentReader.Read(ready[:]); n != 0 || !errors.Is(err, io.EOF) {
					t.Fatal("completed exporter did not close output", n, err)
				}
			}
		})
	}
}

func TestPeerBridgeStdioSubprocess(t *testing.T) {
	mode := os.Getenv("KEELMUX_TEST_BRIDGE_STDIO")
	if mode == "" {
		return
	}
	read := mode == "read"
	original := os.Stdout
	if read {
		original = os.Stdin
	}
	file, err := bridgeStdio(original, read)
	if err != nil {
		_, _ = io.WriteString(os.Stderr, "interruptible stdio setup refused\n")
		os.Exit(2)
	}
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		if read {
			var first [1]byte
			if _, err := io.ReadFull(file, first[:]); err != nil || first[0] != 'A' {
				done <- errors.New("first inherited input missing")
				close(started)
				return
			}
			_, _ = os.Stdout.Write([]byte{'R'})
			close(started)
			_, err := file.Read(first[:])
			done <- err
			return
		}
		if _, err := file.Write([]byte{'R'}); err != nil {
			done <- err
			close(started)
			return
		}
		close(started)
		block := make([]byte, 65536)
		for {
			if _, err := file.Write(block); err != nil {
				done <- err
				return
			}
		}
	}()
	<-started
	// Allow the next read/write to enter the kernel/poller before cancellation.
	time.Sleep(100 * time.Millisecond)
	if err := file.Close(); err != nil {
		_, _ = io.WriteString(os.Stderr, "owned pipe close failed\n")
		os.Exit(3)
	}
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrClosed) {
			_, _ = io.WriteString(os.Stderr, "IO ended without explicit pipe cancellation\n")
			os.Exit(4)
		}
	case <-time.After(time.Second):
		_, _ = io.WriteString(os.Stderr, "closing inherited descriptor did not interrupt IO\n")
		os.Exit(5)
	}
	os.Exit(0)
}

func TestPeerBridgeStdioRefusesRegularFilesBeforeEffects(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "not-a-stream")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, read := range []bool{true, false} {
		if pipe, err := bridgeStdio(file, read); pipe != nil || err == nil {
			t.Fatal("non-interruptible file admitted as SSH stream")
		}
	}
	if info, err := file.Stat(); err != nil || info.Size() != 0 {
		t.Fatal("refusal modified or closed caller-owned file", err)
	}
	if pipe, err := bridgeStdio(nil, true); pipe != nil || err == nil {
		t.Fatal("missing stream admitted")
	}
}

func TestPeerBridgeStdioRefusesWrongPipeDirection(t *testing.T) {
	in, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer out.Close()
	for _, c := range []struct {
		file *os.File
		read bool
	}{{in, false}, {out, true}} {
		if pipe, err := bridgeStdio(c.file, c.read); pipe != nil || err == nil {
			t.Fatal("wrong stream direction admitted before home effects")
		}
		if _, err := c.file.Stat(); err != nil {
			t.Fatal("refusal closed the caller-owned stream", err)
		}
	}
}
