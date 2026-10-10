package nativepty

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

// outputFilter uses the same standards-aware parser as the terminal tracker.
// Only regenerated complete controls reach clients: C0 inside ESC/CSI, raw C1
// framing and UTF-8 continuation bytes cannot escape the control policy.
type outputFilter struct {
	parser           *ansi.Parser
	visible, replies []byte
}

func (s *terminalScreen) initFilter() {
	f := &s.filter
	f.parser = ansi.NewParser()
	f.parser.SetDataSize(65536)
	f.parser.SetParamsSize(32)
	control := func(sequence []byte, blocked bool) {
		before := s.emulator.CursorPosition()
		_, _ = s.emulator.Write(sequence)
		after := s.emulator.CursorPosition()
		// Positioning/erasure operations terminate the streaming grapheme tail.
		// SGR and queries preserve delayed wrap, as real terminals do.
		preserveWrap := len(sequence) > 2 && strings.ContainsRune("mnctp", rune(sequence[len(sequence)-1]))
		// Character-set selection/shifts, nonmoving controls, and a tab at
		// the right edge leave delayed wrap intact. Clearing only our shadow flag
		// makes the next ASCII batch calculate its tail one column too far.
		if len(sequence) == 1 && strings.ContainsRune("\x00\a\t\x0e\x0f\x7f", rune(sequence[0])) {
			preserveWrap = true
		} else if len(sequence) == 3 && sequence[0] == 0x1b && strings.ContainsRune("()*+", rune(sequence[1])) {
			preserveWrap = true
		} else if len(sequence) == 2 && sequence[0] == 0x1b && strings.ContainsRune("78=><HNOno|}~", rune(sequence[1])) {
			preserveWrap = true
		} else if len(sequence) > 2 && sequence[0] == 0x1b && sequence[1] == ']' {
			preserveWrap = true
		}
		s.glyph = ""
		if before != after || !preserveWrap {
			s.pendingWrap = false
		}
		answer, query := s.query(sequence)
		if query {
			if len(f.replies)+len(answer) <= maxInput {
				f.replies = append(f.replies, answer...)
			} else {
				s.degraded = true
			}
		} else if !blocked {
			f.visible = append(f.visible, sequence...)
		}
	}
	f.parser.SetHandler(ansi.Handler{
		Print: func(r rune) { s.writeGlyph(r); f.visible = utf8.AppendRune(f.visible, r) },
		Execute: func(b byte) {
			// Normalize supported C1 cursor controls into unambiguous seven-bit form.
			switch b {
			case 0x84:
				control([]byte("\x1bD"), false)
			case 0x85:
				control([]byte("\x1bE"), false)
			case 0x8d:
				control([]byte("\x1bM"), false)
			default:
				if b < 0x20 || b == 0x7f {
					control([]byte{b}, false)
				}
			}
		},
		HandleEsc: func(cmd ansi.Cmd) {
			if cmd.Final() == '\\' && cmd.Intermediate() == 0 {
				return
			}
			var seq []byte
			seq = append(seq, 0x1b)
			if c := cmd.Intermediate(); c != 0 {
				seq = append(seq, byte(c))
			}
			seq = append(seq, byte(cmd.Final()))
			control(seq, false)
		},
		HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
			var out strings.Builder
			out.WriteString("\x1b[")
			if c := cmd.Prefix(); c != 0 {
				out.WriteByte(byte(c))
			}
			params.ForEach(-1, func(i, p int, more bool) {
				if p >= 0 {
					fmt.Fprintf(&out, "%d", p)
				}
				if i < len(params)-1 {
					if more {
						out.WriteByte(':')
					} else {
						out.WriteByte(';')
					}
				}
			})
			if c := cmd.Intermediate(); c != 0 {
				out.WriteByte(byte(c))
			}
			out.WriteByte(byte(cmd.Final()))
			control([]byte(out.String()), false)
		},
		HandleOsc: func(_ int, data []byte) {
			if len(data) >= 65536 {
				s.degraded = true
				return
			}
			seq := append([]byte("\x1b]"), data...)
			seq = append(seq, 0x1b, '\\')
			control(seq, true)
		},
		HandleDcs: func(_ ansi.Cmd, _ ansi.Params, data []byte) {
			if len(data) >= 65536 {
				s.degraded = true
			}
		},
		HandleApc: func(data []byte) {
			if len(data) >= 65536 {
				s.degraded = true
			}
		},
		HandlePm: func(data []byte) {
			if len(data) >= 65536 {
				s.degraded = true
			}
		},
		HandleSos: func(data []byte) {
			if len(data) >= 65536 {
				s.degraded = true
			}
		},
	})
}
func (s *terminalScreen) process(data []byte) (visible, replies []byte) {
	if s.filter.parser == nil {
		s.initFilter()
	}
	s.filter.visible = nil
	s.filter.replies = nil
	for len(data) > 0 {
		// Only ground-state printable ASCII can bypass control framing. In
		// particular, bytes inside a split OSC/CSI/UTF-8 sequence must still
		// pass through the parser, even when those bytes happen to be ASCII.
		if s.filter.parser.State() == parser.GroundState && data[0] >= 0x20 && data[0] < 0x7f {
			n := 1
			for n < len(data) && data[n] >= 0x20 && data[n] < 0x7f {
				n++
			}
			s.writeASCII(data[:n])
			s.filter.visible = append(s.filter.visible, data[:n]...)
			data = data[n:]
			continue
		}
		s.filter.parser.Advance(data[0])
		data = data[1:]
	}
	return s.filter.visible, s.filter.replies
}

// writeASCII gives the emulator whole runs so it can use its ASCII grapheme
// path instead of segmenting and allocating once per byte. The first byte still
// uses the streaming Unicode tail (a preceding Prepend rune can join it). Two
// consecutive printable ASCII bytes always have a grapheme boundary. Retain the
// last byte's exact cell so a combining mark in the next PTY read can extend it.
func (s *terminalScreen) writeASCII(data []byte) {
	s.writeGlyph(rune(data[0]))
	data = data[1:]
	if len(data) == 0 {
		return
	}
	x := s.emulator.CursorPosition().X
	width := s.emulator.Width()
	if s.pendingWrap && s.modes[7] {
		x++ // the cursor is logically one column beyond the right edge
	}
	_, _ = s.emulator.Write(data)
	pos := s.emulator.CursorPosition()
	s.glyph = string(data[len(data)-1])
	s.glyphY = pos.Y // includes scrolling at the active bottom margin
	if s.modes[7] {
		s.glyphX = (x + len(data) - 1) % width
		s.pendingWrap = s.glyphX == width-1
	} else {
		s.glyphX = min(width-1, x+len(data)-1)
		s.pendingWrap = false
	}
}

// The pinned emulator finalizes its last grapheme at each Write. Keep a bounded
// tail across arbitrary PTY reads; an extending rune replaces that exact cell
// with the complete cluster so accents, VS16 and ZWJ sequences are preserved.
func (s *terminalScreen) writeGlyph(r rune) {
	text := string(r)
	if s.glyph != "" {
		combined := s.glyph + text
		cluster, _ := ansi.FirstGraphemeCluster([]byte(combined), ansi.GraphemeWidth)
		if len(cluster) == len(combined) {
			if len(combined) > 256 {
				s.degraded = true
				return
			}
			row := s.glyphY + 1
			if s.modes[6] {
				row -= s.top - 1
			}
			_, _ = s.emulator.Write([]byte(fmt.Sprintf("\x1b[%d;%dH", row, s.glyphX+1)))
			_, _ = s.emulator.Write([]byte(combined))
			s.glyph = combined
			s.pendingWrap = s.modes[7] && s.glyphX >= s.emulator.Width()-1
			return
		}
	}
	pos := s.emulator.CursorPosition()
	s.glyphX, s.glyphY = pos.X, pos.Y
	if s.pendingWrap && s.modes[7] {
		s.glyphX = 0
		s.glyphY++
		if s.glyphY >= s.emulator.Height() {
			s.glyphY = s.emulator.Height() - 1
		}
	}
	_, _ = s.emulator.Write([]byte(text))
	s.glyph = text
	s.glyphY = s.emulator.CursorPosition().Y
	s.pendingWrap = s.modes[7] && s.glyphX >= s.emulator.Width()-1
}

// query returns only terminal protocol responses; it never routes arbitrary
// output as input. Replies are serialized separately from terminal keystrokes.
func (s *terminalScreen) query(sequence []byte) (answer []byte, query bool) {
	seq := string(sequence)
	if seq == "\x1bZ" {
		return []byte("\x1b[?1;2c"), true
	}
	if strings.HasPrefix(seq, "\x1b[") && len(seq) >= 3 {
		body := seq[2 : len(seq)-1]
		final := seq[len(seq)-1]
		switch final {
		case 'n':
			switch body {
			case "5":
				return []byte("\x1b[0n"), true
			case "6", "?6":
				pos := s.emulator.CursorPosition()
				prefix := ""
				if body == "?6" {
					prefix = "?"
				}
				return []byte(fmt.Sprintf("\x1b[%s%d;%dR", prefix, pos.Y+1, pos.X+1)), true
			default:
				return nil, true
			}
		case 'c':
			if body == "" || body == "0" {
				return []byte("\x1b[?1;2c"), true
			}
			if body == ">" || body == ">0" {
				return []byte("\x1b[>1;0;0c"), true
			}
			return nil, true
		case 't':
			switch body {
			case "18":
				return []byte(fmt.Sprintf("\x1b[8;%d;%dt", s.emulator.Height(), s.emulator.Width())), true
			case "14", "16", "19", "20", "21":
				return nil, true
			}
			return nil, false
		case 'p':
			if strings.HasSuffix(body, "$") {
				text := strings.TrimSuffix(body, "$")
				if strings.HasPrefix(text, "?") {
					mode, err := strconv.Atoi(strings.TrimPrefix(text, "?"))
					setting := 0
					if err == nil {
						if enabled, ok := s.modes[mode]; ok {
							setting = 2
							if enabled {
								setting = 1
							}
						}
						return []byte(fmt.Sprintf("\x1b[?%d;%d$y", mode, setting)), true
					}
				}
				return nil, true
			}
		case 'u':
			if body == "?" {
				return []byte("\x1b[?0u"), true
			}
		case 'q':
			if body == ">" || body == ">0" {
				return []byte("\x1bP>|Keelmux native-vt\x1b\\"), true
			}
		}
	}
	if strings.HasPrefix(seq, "\x1b]") {
		body := strings.TrimSuffix(strings.TrimSuffix(seq[2:], "\x1b\\"), "\a")
		if strings.HasSuffix(body, ";?") {
			cmd := strings.TrimSuffix(body, ";?")
			switch cmd {
			case "10", "11", "12":
				color := s.emulator.ForegroundColor()
				if cmd == "11" {
					color = s.emulator.BackgroundColor()
				}
				if cmd == "12" {
					color = s.emulator.CursorColor()
				}
				if color != nil {
					r, g, b, _ := color.RGBA()
					return []byte(fmt.Sprintf("\x1b]%s;rgb:%04x/%04x/%04x\x1b\\", cmd, r, g, b)), true
				}
			}
			return nil, true
		}
	}
	// Unknown DCS/OSC requests are suppressed and do not manufacture support.
	return nil, false
}
