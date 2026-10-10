package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Command startup must never probe or consume the caller's terminal before
// an explicit attachment owns it. A pipe hides dependency-init terminal IO,
// so exercise the actual executable on an owned PTY without an emulator.
func TestCLIStartupDoesNotQueryCallerTerminal(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "keelmux")
	build := exec.Command("go", "build", "-o", binary, "./cmd/bp")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual CLI: %v %s", err, output)
	}
	for _, args := range [][]string{{"--version"}, {"help"}, {"ircd", "--help"}, {"--entry", "relative", "drive"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = []string{"PATH=" + dir, "HOME=" + dir, "TERM=xterm-256color", "LANG=C.UTF-8"}
			terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			var output bytes.Buffer
			joined := make(chan struct{})
			go func() { _, _ = io.Copy(&output, terminal); close(joined) }()
			err = command.Wait()
			select {
			case <-joined:
			case <-ctx.Done():
				terminal.Close()
				<-joined
				t.Fatal("caller terminal did not settle")
			}
			if ctx.Err() != nil {
				t.Fatal("command startup waited for terminal replies")
			}
			wantFailure := args[0] == "--entry"
			if !wantFailure && err != nil {
				t.Fatalf("information command failed: %v", err)
			}
			if wantFailure && err == nil {
				t.Fatal("invalid drive entry was accepted")
			}
			if bytes.IndexByte(output.Bytes(), 0x1b) >= 0 || bytes.IndexByte(output.Bytes(), 0x9b) >= 0 || bytes.IndexByte(output.Bytes(), 0x9d) >= 0 {
				t.Fatalf("command emitted terminal controls before attachment: %q", output.Bytes())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, ".local")); !os.IsNotExist(err) {
		t.Fatalf("information commands created local state: %v", err)
	}
}
