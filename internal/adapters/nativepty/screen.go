package nativepty

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// terminalScreen tracks current visible cells, independently of the raw output
// ring. Snapshots regenerate printable cells, SGR, cursor and selected modes;
// never replay raw OSC/DCS/APC, clipboard effects, terminal queries or history.
// The terminal emulator is a state tracker, not an input producer.
type terminalScreen struct {
	filter              outputFilter
	glyph               string
	glyphX, glyphY      int
	pendingWrap         bool
	hasLastASCII        bool
	emulator            *vt.Emulator
	modes               map[int]bool
	visible             bool
	pen, savedPen       uv.Style
	mainPen             uv.Style
	mainTop, mainBottom int
	top, bottom         int
	degraded            bool
}

var restoredModes = []int{1, 6, 7, 9, 25, 1000, 1002, 1003, 1004, 1006, 2004}

func newTerminalScreen(cols, rows int) *terminalScreen {
	s := &terminalScreen{emulator: vt.NewEmulator(cols, rows), modes: map[int]bool{7: true, 25: true}, visible: true, top: 1, bottom: rows}
	for _, mode := range restoredModes {
		if _, ok := s.modes[mode]; !ok {
			s.modes[mode] = false
		}
	}
	// The pinned SetScrollbackSize(0) ignores zero and keeps 10,000 main
	// history rows. Reconnect uses current cells only, so disable that unused
	// buffer through its exported zero value; Push is inert at maxLines zero.
	// This accessor covers the main buffer; alternate history stays at the
	// library default. All visible cells and raw output are still processed.
	*s.emulator.Scrollback() = vt.Scrollback{}
	// The tracker must not generate arbitrary responses or block the drain.
	// Our streaming filter answers an explicit protocol allowlist exactly once.
	if closer, ok := s.emulator.InputPipe().(io.Closer); ok {
		_ = closer.Close()
	}
	s.emulator.SetCallbacks(vt.Callbacks{EnableMode: func(mode ansi.Mode) {
		if _, ok := mode.(ansi.DECMode); ok {
			s.modes[mode.Mode()] = true
		}
	}, DisableMode: func(mode ansi.Mode) {
		if _, ok := mode.(ansi.DECMode); ok {
			s.modes[mode.Mode()] = false
		}
	}, CursorVisibility: func(visible bool) { s.visible = visible }, AltScreen: func(on bool) {
		if on {
			s.pendingWrap = false
			s.mainPen = s.pen
			s.mainTop = s.top
			s.mainBottom = s.bottom
			s.top = 1
			s.bottom = s.emulator.Height()
		} else {
			s.pen = s.mainPen
			s.top = s.mainTop
			s.bottom = s.mainBottom
		}
	}})
	s.emulator.RegisterCsiHandler('m', func(params ansi.Params) bool { uv.ReadStyle(params, &s.pen); return false })
	s.emulator.RegisterCsiHandler('r', func(params ansi.Params) bool {
		// Mirror DECSTBM defaults, but refuse out-of-screen margins before the
		// pinned handler installs an unchecked scroll rectangle. Valid margins
		// still use that handler; rejected margins preserve region and cursor.
		top, _, _ := params.Param(0, 1)
		bottom, _, _ := params.Param(1, s.emulator.Height())
		if top < 1 {
			top = 1
		}
		if bottom < 1 {
			bottom = s.emulator.Height()
		}
		if top > s.emulator.Height() || bottom > s.emulator.Height() {
			return true
		}
		if top < bottom {
			s.top, s.bottom = top, bottom
		}
		return false
	})
	s.emulator.RegisterEscHandler('7', func() bool { s.savedPen = s.pen; return false })
	s.emulator.RegisterEscHandler('8', func() bool { s.pen = s.savedPen; return false })
	s.emulator.RegisterEscHandler('c', func() bool {
		s.pen = uv.Style{}
		s.savedPen = uv.Style{}
		s.top = 1
		s.bottom = s.emulator.Height()
		return false
	})
	return s
}
func (s *terminalScreen) write(data []byte) { _, _ = s.process(data) }
func (s *terminalScreen) resize(cols, rows int) {
	s.emulator.Resize(cols, rows)
	s.glyph = ""
	s.pendingWrap = false
	s.top = 1
	s.bottom = rows
}
func (s *terminalScreen) close() { _ = s.emulator.Close() }
func (s *terminalScreen) snapshot(writer bool) []byte {
	var out strings.Builder
	// Restore active buffer explicitly. The snapshot preserves visible state;
	// the inactive buffer, application-specific scrollback and saved cursor
	// stacks are outside this initial reconnect contract.
	if s.emulator.IsAltScreen() {
		out.WriteString("\x1b[?1049h")
	} else {
		out.WriteString("\x1b[?1049l")
	}
	out.WriteString("\x1b[?6l\x1b[?7l\x1b[r\x1b[0m\x1b[2J\x1b[H")
	var style uv.Style
	for y := 0; y < s.emulator.Height(); y++ {
		fmt.Fprintf(&out, "\x1b[%d;1H", y+1)
		for x := 0; x < s.emulator.Width(); {
			c := s.emulator.CellAt(x, y)
			if c == nil {
				out.WriteByte(' ')
				x++
				continue
			}
			if c.Width == 0 {
				x++
				continue
			}
			if !style.Equal(&c.Style) {
				out.WriteString("\x1b[0m")
				out.WriteString(c.Style.String())
				style = c.Style
			}
			content := strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return -1
				}
				return r
			}, c.Content)
			if len(content) > 256 {
				s.degraded = true
				content = "�"
			}
			if content == "" {
				content = " "
			}
			out.WriteString(content)
			x += c.Width
			if out.Len() > 1<<20 {
				s.degraded = true
				return []byte("\x1b[0m\x1b[2J\x1b[H[keelmux: current screen exceeds snapshot bound; waiting for native repaint]\r\n")
			}
		}
	}
	out.WriteString("\x1b[0m")
	if s.top > 0 && s.bottom >= s.top && s.bottom <= s.emulator.Height() {
		fmt.Fprintf(&out, "\x1b[%d;%dr", s.top, s.bottom)
	}
	for _, mode := range restoredModes {
		enabled := s.modes[mode]
		if mode == 25 {
			enabled = s.visible
		}
		if !writer && (mode == 9 || mode >= 1000) {
			enabled = false
		}
		suffix := 'l'
		if enabled {
			suffix = 'h'
		}
		fmt.Fprintf(&out, "\x1b[?%d%c", mode, suffix)
	}
	pos := s.emulator.CursorPosition()
	row := pos.Y + 1
	if s.modes[6] {
		row -= s.top - 1
	}
	fmt.Fprintf(&out, "\x1b[%d;%dH", row, pos.X+1)
	if s.pendingWrap {
		if cell := s.emulator.CellAt(pos.X, pos.Y); cell != nil && cell.Width == 1 {
			out.WriteString(cell.Style.String())
			out.WriteString(cell.Content)
		}
	}
	out.WriteString(s.pen.String())
	return []byte(out.String())
}
