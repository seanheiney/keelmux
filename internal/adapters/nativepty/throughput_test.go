package nativepty

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireNoMainHistory(t *testing.T, screen *terminalScreen) {
	t.Helper()
	history := screen.emulator.Scrollback()
	if history == nil {
		t.Fatal("current-screen tracker lost its exported main history handle")
	}
	if history.MaxLines() != 0 || history.Len() != 0 {
		t.Fatalf("current-screen tracker retained unused main history: max=%d rows=%d", history.MaxLines(), history.Len())
	}
}

// Main history is not part of reconnect snapshots. Verify that removing its
// storage leaves the entire visible state and protocol stream identical to a
// history-enabled reference through scrolling and state-changing controls.
// The public accessor is main-only; this test makes no alternate-history claim.
func TestCurrentScreenWithoutMainHistoryMatchesHistoryEnabled(t *testing.T) {
	for _, size := range [][2]int{{1, 2}, {2, 3}, {7, 4}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			screen, reference := newTerminalScreen(size[0], size[1]), newTerminalScreen(size[0], size[1])
			defer screen.close()
			defer reference.close()
			reference.emulator.SetScrollbackSize(32)
			check := func(data []byte) {
				t.Helper()
				// Compare every split's visible/reply stream, then all cells at
				// the logical-step boundary. Rendering four full snapshots for
				// every tiny chunk would turn the 80x24 control into ~90M cell
				// visits rather than testing a distinct state transition.
				for offset := 0; offset < len(data); offset += 13 {
					part := data[offset:min(offset+13, len(data))]
					visible, replies := screen.process(part)
					wantVisible, wantReplies := reference.process(part)
					if !bytes.Equal(visible, wantVisible) || !bytes.Equal(replies, wantReplies) {
						t.Fatal("disabling main history changed visible bytes or terminal replies")
					}
				}
				for _, writer := range []bool{false, true} {
					if !bytes.Equal(screen.snapshot(writer), reference.snapshot(writer)) {
						t.Fatalf("disabling main history changed cells/cursor/styles/modes (writer=%v)", writer)
					}
				}
				requireNoMainHistory(t, screen)
			}
			seed := strings.Repeat("abcXYZ123 ", size[0]*size[1])
			check([]byte(seed))
			if reference.emulator.ScrollbackLen() == 0 {
				t.Fatal("history-enabled reference did not exercise row retention")
			}
			steps := []string{
				"\x1b[31m" + seed + "e\u0301🙂👩\u200d💻\x1b[0m\x1b[6n",
				"\x1b[2;3r\x1b[?6h\x1b[1;1H" + seed + "\u0301\x1b[6n",
				"\x1b[?6l\x1b[r\x1b(0" + seed + "lqk\x1b(B\x1b[6n",
				"\x1b[4h\x1b[1;1HINSERT\x1b[4l\x1b[2@\x1b[P\x1b[6n",
				"\x1b[?7l" + seed + "e\u0301\x1b[?7h\x1b[6n",
				"\x1b[2J" + seed + "\x1b[3J\x1b[6n",
				"\x1b[?1049h\x1b[32m" + seed + "❤️\x1b[6n\x1b[?1049l\x1b[6n",
				"\x1bc" + seed + "e\u0301\x1b[6n\x1b[c",
			}
			for _, step := range steps {
				// Splits include ESC/CSI and UTF-8 continuations, not just ASCII.
				check([]byte(step))
			}
			for _, resized := range [][2]int{{3, 2}, {80, 24}, size} {
				screen.resize(resized[0], resized[1])
				reference.resize(resized[0], resized[1])
				check([]byte(seed + "e\u0301\x1b[6n"))
			}
		})
	}
}

// Keep a real viewer unread throughout the same 2 MiB native PTY burst as the
// original eight-second capacity gate. A post-tail CPR proves the full final
// Unicode cell was tracked before the receipt; reconnect must preserve it.
func TestSlowViewerBurstTracksCompleteUnicodeTail(t *testing.T) {
	b := testBackend(t, 4096)
	marker := filepath.Join(b.state, "burst-tracked")
	spec := testSpec(`stty -echo; IFS= read -r line; stty raw -echo; head -c 2097152 /dev/zero | tr '\000' x; printf '\r\nDONE-e\314\201\033[6n'; answer=$(dd bs=1 count=7 2>/dev/null); printf '%s' "$answer" > "$1.pending"; mv "$1.pending" "$1"; exec sleep 30`)
	spec.Argv = append(spec.Argv, "test", marker)
	diagnostic := prepareCapacityFixture(t, b, marker, &spec)
	ref := launchTest(t, b, spec, false)
	slow := openWriter(t, b, ref)
	if err := slow.WriteInput([]byte("burst\n")); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	diagnostic.begin(started)
	defer diagnostic.collect(t, b, ref)
	deadline := started.Add(8 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		diagnostic.poll()
		if err == nil {
			if string(data) != "\x1b[24;7R" {
				t.Fatalf("full burst tail cursor reply %q", data)
			}
			break
		}
		if time.Now().After(deadline) {
			diagnostic.failed = true
			t.Fatal("unread viewer blocked full native burst and tracked Unicode tail")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("native_burst_bytes=2097152 unread_viewer=true complete_tail_ack_seconds=%.3f", time.Since(started).Seconds())
	current, err := b.Open(context.Background(), ref, true, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if !bytes.Contains(current.Snapshot, []byte("DONE-e\u0301")) {
		t.Fatalf("reconnect lost fully tracked Unicode tail: %q", current.Snapshot)
	}
	o, err := b.Inspect(context.Background(), ref)
	if err != nil || !o.Alive {
		t.Fatalf("native burst lost the owned child: %+v %v", o, err)
	}
}
