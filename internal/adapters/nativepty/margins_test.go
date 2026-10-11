package nativepty

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

func TestDECSTBMOutOfScreenDeleteLineReproducer(t *testing.T) {
	s := newTerminalScreen(1, 2)
	defer s.close()
	input := []byte("\x1b[2;3r\x1b[2;1H\x1b[M")
	if len(input) != 15 {
		t.Fatal("reproducer changed")
	}
	visible, replies := s.process(input)
	if !bytes.Equal(visible, input) || len(replies) != 0 {
		t.Fatalf("visible/replies changed: %q %q", visible, replies)
	}
	if s.top != 1 || s.bottom != 2 || s.emulator.CursorPosition().X != 0 || s.emulator.CursorPosition().Y != 1 {
		t.Fatal("invalid margins changed current region/cursor")
	}
}

func sameMarginScreen(t *testing.T, actual, reference *terminalScreen) {
	t.Helper()
	if actual.emulator.String() != reference.emulator.String() || actual.emulator.CursorPosition() != reference.emulator.CursorPosition() || actual.top != reference.top || actual.bottom != reference.bottom || actual.pendingWrap != reference.pendingWrap {
		t.Fatalf("state differs: cells %q/%q cursor %v/%v region %d,%d/%d,%d wrap %v/%v", actual.emulator.String(), reference.emulator.String(), actual.emulator.CursorPosition(), reference.emulator.CursorPosition(), actual.top, actual.bottom, reference.top, reference.bottom, actual.pendingWrap, reference.pendingWrap)
	}
	for _, writer := range []bool{false, true} {
		if !bytes.Equal(actual.snapshot(writer), reference.snapshot(writer)) {
			t.Fatalf("snapshot differs writer=%v", writer)
		}
	}
}
func TestDECSTBMInvalidBoundsPreservePriorStateAndReplies(t *testing.T) {
	for _, margin := range []string{"\x1b[2;5r", "\x1b[5;6r", "\x1b[0;9r", "\x1b[9999999;9999999r", "\x1b[1;1r", "\x1b[4;2r"} {
		t.Run(fmt.Sprintf("%x", margin), func(t *testing.T) {
			s := newTerminalScreen(6, 4)
			defer s.close()
			s.process([]byte("\x1b[2;4r\x1b[?6h\x1b[2;3H"))
			before := s.snapshot(true)
			cursor := s.emulator.CursorPosition()
			v, r := s.process([]byte(margin))
			if string(v) != margin || len(r) != 0 || s.emulator.CursorPosition() != cursor || s.top != 2 || s.bottom != 4 || !bytes.Equal(s.snapshot(true), before) {
				t.Fatal("rejected margin mutated state or output")
			}
			_, r = s.process([]byte("\x1b[6n"))
			if string(r) != fmt.Sprintf("\x1b[%d;%dR", cursor.Y+1, cursor.X+1) {
				t.Fatalf("CPR changed %q", r)
			}
		})
	}
}
func TestDECSTBMValidAndDefaultMarginsMatchPinnedParser(t *testing.T) {
	for _, tc := range []struct {
		seq         string
		top, bottom int
	}{{"\x1b[r", 1, 5}, {"\x1b[;r", 1, 5}, {"\x1b[0;0r", 1, 5}, {"\x1b[0;4r", 1, 4}, {"\x1b[2;r", 2, 5}, {"\x1b[;4r", 1, 4}, {"\x1b[1;5r", 1, 5}, {"\x1b[2;4r", 2, 4}} {
		for _, origin := range []bool{false, true} {
			t.Run(fmt.Sprintf("%x/origin=%v", tc.seq, origin), func(t *testing.T) {
				s := newTerminalScreen(6, 5)
				defer s.close()
				reference := vt.NewEmulator(6, 5)
				defer reference.Close()
				if closer, ok := reference.InputPipe().(io.Closer); ok {
					_ = closer.Close()
				}
				prefix := "seed\x1b[4;3H"
				if origin {
					prefix += "\x1b[?6h"
				}
				s.process([]byte(prefix))
				reference.Write([]byte(prefix))
				v, r := s.process([]byte(tc.seq))
				reference.Write([]byte(tc.seq))
				if string(v) != tc.seq || len(r) != 0 || s.top != tc.top || s.bottom != tc.bottom || s.emulator.String() != reference.String() || s.emulator.CursorPosition() != reference.CursorPosition() {
					t.Fatal("valid/default margin diverged from pinned parser")
				}
			})
		}
	}
}
func TestDECSTBMRejectedMarginsPreserveDelayedWrapAndUnicode(t *testing.T) {
	for _, size := range [][2]int{{1, 2}, {2, 3}, {7, 5}, {80, 24}} {
		for _, chunk := range []int{1, 2, 7, 65536} {
			t.Run(fmt.Sprintf("%dx%d/chunk%d", size[0], size[1], chunk), func(t *testing.T) {
				s, reference := newTerminalScreen(size[0], size[1]), newTerminalScreen(size[0], size[1])
				defer s.close()
				defer reference.close()
				prefix := strings.Repeat("a", size[0])
				s.process([]byte(prefix))
				reference.process([]byte(prefix))
				if !s.pendingWrap {
					t.Fatal("test did not establish delayed wrap")
				}
				invalid := "\x1b[2;999r"
				tail := "e\u0301🙂\x1b[6n\r\n世界\x1b[2;1H\x1b[M\x1b[L\x1b[S\x1b[T\x1bD\x1bM\x1b[6n"
				trace := []byte(invalid + tail)
				var visible, replies []byte
				for i := 0; i < len(trace); i += chunk {
					v, r := s.process(trace[i:min(i+chunk, len(trace))])
					visible = append(visible, v...)
					replies = append(replies, r...)
				}
				wantVisible, wantReplies := reference.process([]byte("\x1b[1;1r" + tail))
				wantVisible = bytes.Replace(wantVisible, []byte("\x1b[1;1r"), []byte(invalid), 1)
				if !bytes.Equal(visible, wantVisible) || !bytes.Equal(replies, wantReplies) {
					t.Fatalf("visible/query stream differs %q/%q %q/%q", visible, wantVisible, replies, wantReplies)
				}
				sameMarginScreen(t, s, reference)
			})
		}
	}
}
func TestDECSTBMBoundsUseCurrentHeightAfterResize(t *testing.T) {
	s, reference := newTerminalScreen(7, 5), newTerminalScreen(7, 5)
	defer s.close()
	defer reference.close()
	prefix := "\x1b[2;5r\x1b[?6h\x1b[2;2H世界"
	s.process([]byte(prefix))
	reference.process([]byte(prefix))
	s.resize(7, 3)
	reference.resize(7, 3)
	cursor := s.emulator.CursorPosition()
	v, r := s.process([]byte("\x1b[2;5r"))
	reference.process([]byte("\x1b[1;1r"))
	if string(v) != "\x1b[2;5r" || len(r) > 0 || s.emulator.CursorPosition() != cursor || s.top != 1 || s.bottom != 3 {
		t.Fatal("resize accepted stale-height region")
	}
	sameMarginScreen(t, s, reference)
	s.resize(7, 6)
	reference.resize(7, 6)
	tail := []byte("\x1b[2;5r\x1b[1;1He\u0301\x1b[6n")
	v, r = s.process(tail)
	wv, wr := reference.process(tail)
	if !bytes.Equal(v, wv) || !bytes.Equal(r, wr) {
		t.Fatal("newly in-bounds region rejected after growth")
	}
	sameMarginScreen(t, s, reference)
}
func TestDECSTBMMalformedBoundsAreSafeThroughScrollOperations(t *testing.T) {
	for rows := 1; rows <= 5; rows++ {
		for top := 0; top <= rows+2; top++ {
			for bottom := 0; bottom <= rows+2; bottom++ {
				s := newTerminalScreen(2, rows)
				seq := fmt.Sprintf("\x1b[%d;%dr\x1b[?6h\x1b[2;1H\x1b[M\x1b[L\x1b[S\x1b[T\x1bD\x1bMabc\u0301\x1b[6n", top, bottom)
				_, r := s.process([]byte(seq))
				if s.top < 1 || s.bottom > rows || s.top > s.bottom || len(r) == 0 {
					s.close()
					t.Fatalf("unsafe/unfinished rows%d margins%d;%d", rows, top, bottom)
				}
				s.close()
			}
		}
	}
}
func TestDECSTBMAltScreenRetainsEachValidRegion(t *testing.T) {
	s, reference := newTerminalScreen(7, 5), newTerminalScreen(7, 5)
	defer s.close()
	defer reference.close()
	for _, step := range []struct{ actual, reference string }{{"\x1b[2;5r\x1b[?6h\x1b[1;1Hmain", "\x1b[2;5r\x1b[?6h\x1b[1;1Hmain"}, {"\x1b[?1049h\x1b[2;4r\x1b[1;1Halt", "\x1b[?1049h\x1b[2;4r\x1b[1;1Halt"}, {"\x1b[2;9r\x1b[1;1He\u0301\x1b[6n", "\x1b[1;1r\x1b[1;1He\u0301\x1b[6n"}, {"\x1b[?1049l\x1b[2;9r\x1b[6n", "\x1b[?1049l\x1b[1;1r\x1b[6n"}} {
		_, r := s.process([]byte(step.actual))
		_, wr := reference.process([]byte(step.reference))
		if !bytes.Equal(r, wr) {
			t.Fatal("alternate cursor reply changed")
		}
		sameMarginScreen(t, s, reference)
	}
}
