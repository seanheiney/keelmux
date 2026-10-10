package nativepty

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// These are expected cells/cursors, not batch-versus-bytewise comparisons:
// both processing paths previously shared the same wrong short-tail location.
func TestShortGraphemeAfterPendingWrapControl(t *testing.T) {
	controls := []string{"\x1b(B", "\x0f", "\t", "\x1b[?25l", "\x1b[2J", "\x1b[2K", "\x1b[?7l\x1b[?7h", "\x1b7", "\x1b[31m"}
	tails := []struct {
		text  string
		cells []string
		width int
	}{
		{"A\u0301", []string{"A\u0301"}, 1},
		{"AB\u0301", []string{"A", "B\u0301"}, 1},
		{"\u0600A\u0301", []string{"\u0600A\u0301"}, 1},
		{"A\u200d", []string{"A\u200d"}, 1},
		{"👩\u200d💻", []string{"👩\u200d💻"}, 2},
	}
	contexts := []struct {
		name, setup    string
		startY, bottom int
	}{
		{"top", "", 0, 3},
		{"bottom", "\x1b[4;1H", 3, 3},
		{"origin-margin", "\x1b[2;3r\x1b[?6h\x1b[2;1H", 2, 2},
	}
	for _, cols := range []int{1, 2, 7} {
		for _, context := range contexts {
			for _, control := range controls {
				for _, tail := range tails {
					for _, chunk := range []int{1, 2, 5, 128} {
						name := fmt.Sprintf("cols%d/%s/%q/%q/chunk%d", cols, context.name, control, tail.text, chunk)
						t.Run(name, func(t *testing.T) {
							s := newTerminalScreen(cols, 4)
							defer s.close()
							input := []byte(context.setup + strings.Repeat("x", cols) + control + tail.text)
							for offset := 0; offset < len(input); offset += chunk {
								s.process(input[offset:min(offset+chunk, len(input))])
							}
							y := min(context.startY+1, context.bottom)
							x := 0
							for index, content := range tail.cells {
								if index > 0 && x == cols {
									x = 0
									y = min(y+1, context.bottom)
								}
								// A two-column glyph cannot fit a one-column PTY;
								// the pinned screen intentionally stores a blank.
								wantWidth := tail.width
								if tail.width > cols {
									content, wantWidth = " ", 1
								}
								// In a one-column bottom row the next glyph scrolls
								// this cell up; inspect its final position explicitly.
								cellY := y
								if cols == 1 && index == 0 && len(tail.cells) == 2 && y == context.bottom {
									cellY--
								}
								cell := s.emulator.CellAt(x, cellY)
								if cell == nil || cell.Content != content || cell.Width != wantWidth {
									t.Fatalf("cell(%d,%d)=%+v, want %q width%d; screen=%q", x, cellY, cell, content, wantWidth, s.emulator.String())
								}
								x += tail.width
							}
							wantWrap := (x - tail.width) >= cols-1
							wantCursor := uv.Pos(min(x, cols-1), y)
							if got := s.emulator.CursorPosition(); got != wantCursor || s.pendingWrap != wantWrap {
								t.Fatalf("cursor=%v wrap=%v, want %v wrap=%v", got, s.pendingWrap, wantCursor, wantWrap)
							}
							if context.name == "origin-margin" {
								for _, outside := range []int{0, 3} {
									for col := 0; col < cols; col++ {
										cell := s.emulator.CellAt(col, outside)
										if cell != nil && cell.Content != " " && cell.Content != "" {
											t.Fatalf("glyph escaped origin margins: cell(%d,%d)=%+v", col, outside, cell)
										}
									}
								}
							}
						})
					}
				}
			}
		}
	}
}

func TestPendingWrapControlSemantics(t *testing.T) {
	cases := []struct {
		name, input, want string
		x, y              int
		wrap              bool
	}{
		{"erase-char-resets", "xx\x1b[XA\u0301", "xA\u0301", 1, 0, true},
		{"erase-line-zero-resets", "xx\x1b[KA\u0301", "xA\u0301", 1, 0, true},
		{"clamped-motion-resets", "xx\x1b[CA\u0301", "xA\u0301", 1, 0, true},
		{"repeat-sets-wrap", "x\x1b[bA\u0301", "xx\nA\u0301", 1, 1, false},
		{"restore-preserves-wrap", "\x1b7xx\x1b8A\u0301", "xx\nA\u0301", 1, 1, false},
		{"backtab-preserves-wrap", "xx\x1b[ZA\u0301", "xx\nA\u0301", 1, 1, false},
		{"invalid-margins-preserve", "xx\x1b[2;1rA\u0301", "xx\nA\u0301", 1, 1, false},
		{"reverse-index-preserves", "\x1b[2;1Hxx\x1bMA\u0301", "\nA\u0301x", 1, 1, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s := newTerminalScreen(2, 4)
			defer s.close()
			s.process([]byte(test.input))
			if got := strings.TrimRight(s.emulator.String(), "\n"); got != test.want {
				t.Fatalf("screen=%q want=%q", got, test.want)
			}
			if got := s.emulator.CursorPosition(); got != uv.Pos(test.x, test.y) || s.pendingWrap != test.wrap {
				t.Fatalf("cursor=%v wrap=%v", got, s.pendingWrap)
			}
		})
	}
}

func TestResizeResetsDelayedWrap(t *testing.T) {
	for _, cols := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(cols), func(t *testing.T) {
			s := newTerminalScreen(2, 4)
			defer s.close()
			s.process([]byte("xx"))
			s.resize(cols, 5)
			s.process([]byte("A\u0301"))
			x, y := min(1, cols-1), 0
			if cols == 3 {
				x, y = 2, 0
			}
			if cell := s.emulator.CellAt(x, y); cell == nil || cell.Content != "A\u0301" {
				t.Fatalf("cell(%d,%d)=%+v", x, y, cell)
			}
			if got := s.emulator.CursorPosition(); got != uv.Pos(min(x+1, cols-1), y) || s.pendingWrap != (x == cols-1) {
				t.Fatalf("cursor=%v wrap=%v", got, s.pendingWrap)
			}
		})
	}
}
