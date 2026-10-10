package nativepty

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestASCIIBatchMatchesBytewise(t *testing.T) {
	cases := []string{
		strings.Repeat("abcXYZ123 ", 31) + "e\u0301",
		"1234567\u0301x\x1b[6n",
		"123456\u0600A\u0301bc", // a Prepend rune may join the next ASCII rune
		"\x1b[2;3r\x1b[?6h\x1b[2;1H" + strings.Repeat("x", 21) + "\u0301z\x1b[6n",
		"\x1b[?7l" + strings.Repeat("x", 24) + "e\u0301\x1b[6n",
		"\x1b[31m1234567\x1b[0m89\x1b[?1049h" + strings.Repeat("A", 36) + "\x1b[?1049lxyz",
		"\x1b(0lqk\x1b(Bascii\x1b]52;c;forbidden\aok\x1b[6n",
		"before\x1b\r]52;c;forbidden\aafter\x9d52;c;blocked\x9cmore\x9b6n",
		"a\x1bPprivate-ascii\x1b\\b\x1b_ASCII-APC\x1b\\c\x1b[1;31mred",
		"🙂abc❤️def👩\u200d💻ghi👍🏽jkl",
	}
	for index, text := range cases {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			input := []byte(text)
			reference := newTerminalScreen(7, 4)
			defer reference.close()
			var wantVisible, wantReplies []byte
			for _, value := range input {
				visible, replies := reference.process([]byte{value})
				wantVisible = append(wantVisible, visible...)
				wantReplies = append(wantReplies, replies...)
			}
			wantSnapshot := reference.snapshot(true)
			for _, chunk := range []int{2, 5, 16, len(input)} {
				t.Run(fmt.Sprint(chunk), func(t *testing.T) {
					screen := newTerminalScreen(7, 4)
					defer screen.close()
					var gotVisible, gotReplies []byte
					for offset := 0; offset < len(input); offset += chunk {
						visible, replies := screen.process(input[offset:min(offset+chunk, len(input))])
						gotVisible = append(gotVisible, visible...)
						gotReplies = append(gotReplies, replies...)
					}
					if !bytes.Equal(gotVisible, wantVisible) || !bytes.Equal(gotReplies, wantReplies) {
						t.Fatalf("batch changed visible/query bytes: %q/%q versus %q/%q", gotVisible, gotReplies, wantVisible, wantReplies)
					}
					if got := screen.snapshot(true); !bytes.Equal(got, wantSnapshot) {
						t.Fatalf("batch changed screen/cursor/modes: %q versus %q", got, wantSnapshot)
					}
				})
			}
		})
	}
}

func BenchmarkScreenASCIIBurst(b *testing.B) {
	data := bytes.Repeat([]byte("x"), 32<<10)
	s := newTerminalScreen(80, 24)
	defer s.close()
	b.SetBytes(2 << 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for n := 0; n < 64; n++ {
			s.process(data)
		}
	}
}

func TestASCIITailCombinesAtActiveScrollMargin(t *testing.T) {
	screen := newTerminalScreen(7, 4)
	defer screen.close()
	screen.process([]byte("\x1b[2;3r\x1b[?6h\x1b[2;1H" + strings.Repeat("x", 21)))
	screen.process([]byte("\u0301"))
	cell := screen.emulator.CellAt(6, 2)
	if cell == nil || cell.Content != "x\u0301" || !screen.pendingWrap {
		t.Fatalf("scrolling lost the final grapheme's actual cell: %+v, wrap=%v", cell, screen.pendingWrap)
	}
	for _, row := range []int{0, 3} {
		for column := 0; column < 7; column++ {
			cell := screen.emulator.CellAt(column, row)
			if cell != nil && cell.Content != " " && cell.Content != "" {
				t.Fatalf("combining tail escaped the active scroll region: %+v", cell)
			}
		}
	}
}
