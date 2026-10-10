package nativepty

import (
	"fmt"
	"strconv"
	"strings"
)

// outputFilter frames terminal controls across arbitrary PTY read boundaries.
// Only completed, bounded sequences reach the tracker. OSC/DCS/APC/PM/SOS are
// state-only (never forwarded to clients); known queries are answered once by
// the supervisor. Original live printable/key-independent bytes are preserved.
type outputFilter struct {
	state    byte
	sequence []byte
	overflow bool
}

func (s *terminalScreen) process(data []byte) (visible, replies []byte) {
	emit := func(p []byte) { _, _ = s.emulator.Write(p); visible = append(visible, p...) }
	complete := func(blocked bool) {
		seq := s.filter.sequence
		if !s.filter.overflow {
			_, _ = s.emulator.Write(seq)
			answer, query := s.query(seq)
			if query {
				if len(replies)+len(answer) <= maxInput {
					replies = append(replies, answer...)
				} else {
					s.degraded = true
				}
			} else if !blocked {
				visible = append(visible, seq...)
			}
		} else {
			s.degraded = true
		}
		s.filter.sequence = s.filter.sequence[:0]
		s.filter.state = 0
		s.filter.overflow = false
	}
	var plain []byte
	flush := func() {
		if len(plain) > 0 {
			emit(plain)
			plain = plain[:0]
		}
	}
	for _, b := range data {
		if s.filter.state == 0 {
			if b == 0x1b {
				flush()
				s.filter.sequence = append(s.filter.sequence[:0], b)
				s.filter.state = 'e'
			} else {
				plain = append(plain, b)
			}
			continue
		}
		if (b == 0x18 || b == 0x1a) && s.filter.state != 'o' && s.filter.state != 's' && s.filter.state != 'O' && s.filter.state != 'S' {
			s.filter = outputFilter{}
			continue
		}
		if b == 0x1b && (s.filter.state == 'c' || s.filter.state == 'i' || s.filter.state == 'e') {
			s.filter.sequence = []byte{b}
			s.filter.state = 'e'
			s.filter.overflow = false
			continue
		}
		if len(s.filter.sequence) < 65536 {
			s.filter.sequence = append(s.filter.sequence, b)
		} else {
			s.filter.overflow = true
		}
		switch s.filter.state {
		case 'e':
			switch b {
			case '[':
				s.filter.state = 'c'
			case ']':
				s.filter.state = 'o'
			case 'P', '_', '^', 'X':
				s.filter.state = 's'
			default:
				if b >= 0x20 && b <= 0x2f {
					s.filter.state = 'i'
				} else {
					complete(false)
				}
			}
		case 'i':
			if b >= 0x30 && b <= 0x7e {
				complete(false)
			}
		case 'c':
			if b >= 0x40 && b <= 0x7e {
				complete(false)
			}
		case 'o':
			if b == 7 {
				complete(true)
			} else if b == 0x1b {
				s.filter.state = 'O'
			}
		case 's':
			if b == 0x1b {
				s.filter.state = 'S'
			}
		case 'O':
			if b == '\\' {
				complete(true)
			} else if b != 0x1b {
				s.filter.state = 'o'
			}
		case 'S':
			if b == '\\' {
				complete(true)
			} else if b != 0x1b {
				s.filter.state = 's'
			}
		}
	}
	flush()
	return visible, replies
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
