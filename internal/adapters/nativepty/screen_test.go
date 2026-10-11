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

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
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
	if !s.degraded {
		t.Fatal("oversized terminal control unbounded")
	}
}
func TestPreAttachQueriesBecomeReady(t *testing.T) {
	b := testBackend(t, 0)
	ready := filepath.Join(b.state, "query-ready")
	// Query answers acknowledge only preceding output. A second CPR after the
	// full marker proves the tracker consumed it before the first attachment;
	// publishing the file before printf races both child output and PTY drain.
	spec := testSpec(`stty raw -echo; printf '\033[H\033[6n'; cpr=$(dd bs=1 count=6 2>/dev/null); printf '\033[c'; da=$(dd bs=1 count=7 2>/dev/null); printf 'READY-WITHOUT-CLIENT\033[6n'; rendered=$(dd bs=1 count=7 2>/dev/null); printf '%s|%s|%s' "$cpr" "$da" "$rendered" > "$1.pending"; mv "$1.pending" "$1"; exec cat`)
	spec.Argv = append(spec.Argv, "test", ready)
	ref := launchTest(t, b, spec, false)
	waitQueryAnswers(t, ready, "\x1b[1;1R|\x1b[?1;2c|\x1b[1;21R")
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

func waitQueryAnswers(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			if string(data) != want {
				t.Fatalf("terminal protocol answers %q", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("pre-attach terminal queries stalled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The old readiness marker acknowledged queries, not text printed afterward.
// An owned FIFO deterministically holds that future text back, without sleeps
// or scheduler assumptions, and proves a blank early snapshot is valid.
func TestQueryAnswersDoNotAcknowledgeFutureOutput(t *testing.T) {
	b := testBackend(t, 0)
	queries, gate, rendered := filepath.Join(b.state, "queries"), filepath.Join(b.state, "render-gate"), filepath.Join(b.state, "rendered")
	if err := unix.Mkfifo(gate, 0600); err != nil {
		t.Fatal(err)
	}
	spec := testSpec(`stty raw -echo; printf '\033[H\033[6n'; cpr=$(dd bs=1 count=6 2>/dev/null); printf '\033[c'; da=$(dd bs=1 count=7 2>/dev/null); printf '%s|%s' "$cpr" "$da" > "$1.pending"; mv "$1.pending" "$1"; IFS= read -r release < "$2"; printf 'READY-WITHOUT-CLIENT\033[6n'; barrier=$(dd bs=1 count=7 2>/dev/null); printf '%s|%s|%s' "$cpr" "$da" "$barrier" > "$3.pending"; mv "$3.pending" "$3"; exec cat`)
	spec.Argv = append(spec.Argv, "test", queries, gate, rendered)
	ref := launchTest(t, b, spec, false)
	waitQueryAnswers(t, queries, "\x1b[1;1R|\x1b[?1;2c")
	s, err := b.Open(context.Background(), ref, true, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if bytes.Contains(s.Snapshot, []byte("READY-WITHOUT-CLIENT")) {
		t.Fatal("snapshot contains output still blocked by the fixture gate")
	}
	release, err := os.OpenFile(gate, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()
	if _, err := release.WriteString("render\n"); err != nil {
		release.Close()
		t.Fatal(err)
	}
	waitQueryAnswers(t, rendered, "\x1b[1;1R|\x1b[?1;2c|\x1b[1;21R")
	if err := release.Close(); err != nil {
		t.Fatal(err)
	}
	readUntil(t, s, "READY-WITHOUT-CLIENT")
	reconnected, err := b.Open(context.Background(), ref, true, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	if !bytes.Contains(reconnected.Snapshot, []byte("READY-WITHOUT-CLIENT")) {
		t.Fatal("acknowledged marker missing from reconnect snapshot")
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

func TestFilterRejectsC0AndC1ControlBypass(t *testing.T) {
	for _, input := range []string{"\x1b\r]52;c;ZXZpbA==\a", "\x9d52;c;ZXZpbA==\x9c", "\x1b\r[6n", "\x9b6n"} {
		s := newTerminalScreen(80, 24)
		var visible, replies []byte
		for _, b := range []byte(input) {
			v, r := s.process([]byte{b})
			visible = append(visible, v...)
			replies = append(replies, r...)
		}
		downstream := vt.NewEmulator(80, 24)
		if c, ok := downstream.InputPipe().(io.Closer); ok {
			_ = c.Close()
		}
		effects := 0
		downstream.RegisterOscHandler(52, func([]byte) bool { effects++; return true })
		downstream.RegisterCsiHandler('n', func(_ ansi.Params) bool { effects++; return true })
		_, _ = downstream.Write(visible)
		downstream.Close()
		s.close()
		if effects != 0 {
			t.Fatalf("control bypass input %q -> %q", input, visible)
		}
		if strings.Contains(input, "6n") && len(replies) == 0 {
			t.Fatalf("query interception bypass %q", input)
		}
	}
	s := newTerminalScreen(80, 24)
	defer s.close()
	v, _ := s.process([]byte("Ý中文"))
	if string(v) != "Ý中文" {
		t.Fatalf("UTF-8 continuation byte treated as C1: %q", v)
	}
}
func TestSnapshotPreservesSplitGraphemeTail(t *testing.T) {
	for _, chunks := range [][]string{{"e", "\u0301", "x"}, {"❤", "\ufe0f", "x"}, {"👩", "\u200d", "💻", "x"}, {"👩", "\U0001f3fd", "\u200d", "💻", "x"}} {
		s := newTerminalScreen(80, 24)
		for _, chunk := range chunks {
			s.process([]byte(chunk))
			s.snapshot(true)
		}
		expected := vt.NewEmulator(80, 24)
		if c, ok := expected.InputPipe().(io.Closer); ok {
			_ = c.Close()
		}
		_, _ = expected.Write([]byte(strings.Join(chunks, "")))
		if got, want := s.emulator.String(), expected.String(); got != want {
			t.Fatalf("split %q got %q want %q", chunks, got, want)
		}
		s.close()
		expected.Close()
	}
}
func TestSnapshotRestoresPendingWrap(t *testing.T) {
	s := newTerminalScreen(5, 4)
	defer s.close()
	s.process([]byte("ABCDE"))
	snapshot := s.snapshot(true)
	client := vt.NewEmulator(5, 4)
	if c, ok := client.InputPipe().(io.Closer); ok {
		_ = c.Close()
	}
	defer client.Close()
	_, _ = client.Write(snapshot)
	v, _ := s.process([]byte("F"))
	_, _ = client.Write(v)
	if client.String() != s.emulator.String() {
		t.Fatalf("pending wrap lost: %q vs %q", client.String(), s.emulator.String())
	}
}
func TestSnapshotRestoresMainPenAfterAlternateScreen(t *testing.T) {
	s := newTerminalScreen(40, 10)
	defer s.close()
	s.process([]byte("\x1b[31mA\x1b[?1049h\x1b[32mB\x1b[?1049l"))
	client := vt.NewEmulator(40, 10)
	if c, ok := client.InputPipe().(io.Closer); ok {
		_ = c.Close()
	}
	defer client.Close()
	_, _ = client.Write(s.snapshot(true))
	v, _ := s.process([]byte("C"))
	_, _ = client.Write(v)
	if !client.CellAt(1, 0).Style.Equal(&s.emulator.CellAt(1, 0).Style) {
		t.Fatal("restored active pen differs after alternate buffer exit")
	}
}
