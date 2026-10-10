package nativepty

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

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

func TestSnapshotUnicodeStyleAltScreenAndControls(t *testing.T) {
	s := newTerminalScreen(80, 24)
	defer s.close()
	text := "\x1b[?1049h\x1b[2J\x1b[3;4H\x1b[1;31m世界 café 🛟\x1b[0m\x1b[?2004h\x1b[?1000h\x1b[?25l\x1b]52;c;ZXZpbA==\a\x1b]8;;https://example.invalid\x1b\\link\x1b]8;;\x1b\\\x1b[6n"
	visible, replies := s.process([]byte(text))
	if bytes.Contains(visible, []byte("\x1b]")) || bytes.Contains(visible, []byte("\x1b[6n")) {
		t.Fatal("query or OSC forwarded")
	}
	if !bytes.Contains(replies, []byte("R")) {
		t.Fatal("CPR unanswered")
	}
	snapshot := s.snapshot(true)
	for _, forbidden := range []string{"\x1b]", "\x1bP", "\x1b[6n", "example.invalid", "ZXZpbA"} {
		if bytes.Contains(snapshot, []byte(forbidden)) {
			t.Fatalf("snapshot replays %q", forbidden)
		}
	}
	if !bytes.Contains(snapshot, []byte("\x1b[?2004h")) || !bytes.Contains(snapshot, []byte("\x1b[?1000h")) {
		t.Fatal("writer modes not restored")
	}
	ro := s.snapshot(false)
	if bytes.Contains(ro, []byte("\x1b[?2004h")) || bytes.Contains(ro, []byte("\x1b[?1000h")) {
		t.Fatal("read-only terminal enabled input modes")
	}
	actual := vt.NewEmulator(80, 24)
	if c, ok := actual.InputPipe().(io.Closer); ok {
		_ = c.Close()
	}
	defer actual.Close()
	_, _ = actual.Write(snapshot)
	if actual.String() != s.emulator.String() {
		t.Fatalf("snapshot differs\nactual=%q\nwant=%q", actual.String(), s.emulator.String())
	}
	if !actual.IsAltScreen() {
		t.Fatal("alternate screen lost")
	}
	if actual.CursorPosition() != s.emulator.CursorPosition() {
		t.Fatal("cursor lost")
	}
	if !actual.CellAt(3, 2).Style.Equal(&s.emulator.CellAt(3, 2).Style) {
		t.Fatal("SGR style lost")
	}
}
func TestProtocolQueriesSplitBoundedAndNoReplay(t *testing.T) {
	s := newTerminalScreen(90, 31)
	defer s.close()
	var visible, replies []byte
	for _, b := range []byte("hello\x1b[6n\x1b[c\x1b[18t\x1b[?2004$p\x1b]11;?\a\x1bPunknown\x1b\\") {
		v, r := s.process([]byte{b})
		visible = append(visible, v...)
		replies = append(replies, r...)
	}
	if string(visible) != "hello" {
		t.Fatalf("filtered output %q", visible)
	}
	for _, want := range []string{"\x1b[1;6R", "\x1b[?1;2c", "\x1b[8;31;90t", "\x1b[?2004;2$y", "\x1b]11;rgb:"} {
		if !bytes.Contains(replies, []byte(want)) {
			t.Fatalf("missing %q in %q", want, replies)
		}
	}
	s.snapshot(true)
	s.snapshot(true)
	_, r := s.process(nil)
	if len(r) != 0 {
		t.Fatal("snapshot reissued protocol query")
	}
	s.process([]byte("\x1b]52;c;" + strings.Repeat("x", 70000) + "\a"))
	if !s.degraded || len(s.filter.sequence) > 65536 {
		t.Fatal("oversized terminal control unbounded")
	}
}
func TestPreAttachQueriesBecomeReady(t *testing.T) {
	b := testBackend(t, 0)
	ready := filepath.Join(b.state, "query-ready")
	spec := testSpec(`stty raw -echo; printf '\033[H\033[6n'; cpr=$(dd bs=1 count=6 2>/dev/null); printf '\033[c'; da=$(dd bs=1 count=7 2>/dev/null); printf '%s|%s' "$cpr" "$da" > "$1"; printf 'READY-WITHOUT-CLIENT'; exec cat`)
	spec.Argv = append(spec.Argv, "test", ready)
	ref := launchTest(t, b, spec, false)
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(ready)
		if err == nil {
			if string(data) != "\x1b[1;1R|\x1b[?1;2c" {
				t.Fatalf("terminal protocol answers %q", data)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pre-attach terminal queries stalled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s := openWriter(t, b, ref)
	if !bytes.Contains(s.Snapshot, []byte("READY-WITHOUT-CLIENT")) {
		t.Fatalf("missing current screen snapshot %q", s.Snapshot)
	}
	if err := s.WriteInput([]byte("future-input")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, s, "future-input")
	s.Close()
	waitNoWriter(t, b, ref)
	s2 := openWriter(t, b, ref)
	if !bytes.Contains(s2.Snapshot, []byte("future-input")) {
		t.Fatal("reconnect visible screen lost")
	}
}
func TestInstalledAttachHelperRawKeysAndDetach(t *testing.T) {
	b := testBackend(t, 0)
	spec := testSpec(`stty raw -echo; printf READY; exec cat`)
	ref := launchTest(t, b, spec, false)
	args, err := b.AttachCommand(ref, false)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(args[0], args[1:]...)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	defer cmd.Process.Kill()
	output := make(chan []byte, 100)
	go func() {
		buffer := make([]byte, 8192)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				output <- append([]byte{}, buffer[:n]...)
			}
			if err != nil {
				close(output)
				return
			}
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		yes, err := ClientAttached(context.Background(), ref, cmd.Process.Pid, false)
		if err == nil && yes {
			break
		}
		if time.Now().After(deadline) {
			select {
			case p := <-output:
				t.Fatalf("attach readiness: %v; output %q", err, p)
			default:
				t.Fatalf("attach readiness: %v", err)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	keys := []byte("héllo 世界 🛟\x1b[A\x1b[200~paste\x1b[201~")
	if _, err = terminal.Write(keys); err != nil {
		t.Fatal(err)
	}
	var observed []byte
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for !bytes.Contains(observed, keys) {
		select {
		case p, ok := <-output:
			if !ok {
				t.Fatalf("helper ended: %q", observed)
			}
			observed = append(observed, p...)
		case <-timeout.C:
			t.Fatalf("raw bytes missing: %q", observed)
		}
	}
	if _, err = terminal.Write([]byte{7, 'd'}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("detach helper did not exit")
	}
	waitNoWriter(t, b, ref)
	o, err := b.Inspect(context.Background(), ref)
	if err != nil || !o.Alive {
		t.Fatalf("detach stopped owned process %+v %v", o, err)
	}
}
func TestSlowReaderDoesNotBlockNativeOutput(t *testing.T) {
	b := testBackend(t, 4096)
	marker := filepath.Join(b.state, "drained")
	spec := testSpec(`stty -echo; IFS= read -r line; head -c 2097152 /dev/zero | tr '\000' x; : > "$1"; exec sleep 30`)
	spec.Argv = append(spec.Argv, "test", marker)
	ref := launchTest(t, b, spec, false)
	s := openWriter(t, b, ref)
	if err := s.WriteInput([]byte("burst\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow viewer blocked child output drain")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = s.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		out, err := s.ReadOutput()
		if err != nil || out.Gap {
			break
		}
		if out.Exited {
			t.Fatal("slow reader killed child")
		}
	}
	o, err := b.Inspect(context.Background(), ref)
	if err != nil || !o.Alive {
		t.Fatalf("native child lost %+v %v", o, err)
	}
}
