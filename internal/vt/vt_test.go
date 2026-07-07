package vt

import (
	"strings"
	"testing"
)

// rowString returns the trimmed text of a grid row.
func rowString(s *Screen, y int) string {
	var b strings.Builder
	w, _ := s.Size()
	for x := 0; x < w; x++ {
		b.WriteRune(s.CellAt(x, y).R)
	}
	return strings.TrimRight(b.String(), " ")
}

func TestPlainText(t *testing.T) {
	s := New(20, 5)
	s.Write([]byte("hello"))
	if got := rowString(s, 0); got != "hello" {
		t.Fatalf("row0 = %q, want %q", got, "hello")
	}
	x, y, _ := s.Cursor()
	if x != 5 || y != 0 {
		t.Fatalf("cursor = (%d,%d), want (5,0)", x, y)
	}
}

func TestNewlineAndCarriageReturn(t *testing.T) {
	s := New(20, 5)
	s.Write([]byte("ab\r\ncd"))
	if got := rowString(s, 0); got != "ab" {
		t.Fatalf("row0 = %q", got)
	}
	if got := rowString(s, 1); got != "cd" {
		t.Fatalf("row1 = %q", got)
	}
}

func TestCursorPositionAndOverwrite(t *testing.T) {
	s := New(20, 5)
	s.Write([]byte("XXXXX"))
	s.Write([]byte("\x1b[1;1H"))   // home
	s.Write([]byte("\x1b[1;3HZ")) // row1 col3 -> Z
	if got := rowString(s, 0); got != "XXZXX" {
		t.Fatalf("row0 = %q, want XXZXX", got)
	}
}

func TestEraseDisplay(t *testing.T) {
	s := New(10, 3)
	s.Write([]byte("aaa\r\nbbb\r\nccc"))
	s.Write([]byte("\x1b[2J")) // clear all
	for y := 0; y < 3; y++ {
		if got := rowString(s, y); got != "" {
			t.Fatalf("row%d = %q, want empty", y, got)
		}
	}
}

func TestEraseLineToEnd(t *testing.T) {
	s := New(10, 2)
	s.Write([]byte("abcdef"))
	s.Write([]byte("\x1b[1;4H")) // col 4 (the 'd')
	s.Write([]byte("\x1b[0K"))   // erase to end of line
	if got := rowString(s, 0); got != "abc" {
		t.Fatalf("row0 = %q, want abc", got)
	}
}

func TestScroll(t *testing.T) {
	s := New(5, 2)
	s.Write([]byte("one\r\ntwo\r\nthree"))
	// after 3 logical lines on a 2-row screen, "one" scrolled off
	if got := rowString(s, 0); got != "two" {
		t.Fatalf("row0 = %q, want two", got)
	}
	if got := rowString(s, 1); got != "three" {
		t.Fatalf("row1 = %q, want three", got)
	}
}

func TestScrollbackRetained(t *testing.T) {
	s := New(5, 2)
	s.Write([]byte("one\r\ntwo\r\nthree\r\nfour"))
	// "one" and "two" scrolled off; live screen shows three/four.
	if s.Scrollback() != 2 {
		t.Fatalf("scrollback len = %d, want 2", s.Scrollback())
	}
	if s.TotalRows() != 4 {
		t.Fatalf("total rows = %d, want 4", s.TotalRows())
	}
	rowText := func(absY int) string {
		var b strings.Builder
		for _, c := range s.RowAt(absY) {
			b.WriteRune(c.R)
		}
		return strings.TrimRight(b.String(), " ")
	}
	if rowText(0) != "one" || rowText(1) != "two" || rowText(2) != "three" || rowText(3) != "four" {
		t.Fatalf("rows = %q/%q/%q/%q", rowText(0), rowText(1), rowText(2), rowText(3))
	}
}

func TestScrollbackBounded(t *testing.T) {
	s := New(4, 1)
	s.maxScroll = 3
	for i := 0; i < 10; i++ {
		s.Write([]byte("x\r\n"))
	}
	if s.Scrollback() > 3 {
		t.Fatalf("scrollback len = %d, want <= 3", s.Scrollback())
	}
}

func TestSGRTracking(t *testing.T) {
	s := New(10, 1)
	s.Write([]byte("\x1b[1;31mR\x1b[0mn"))
	if a := s.CellAt(0, 0).A; !a.Bold || a.FG != "31" {
		t.Fatalf("cell0 attr = %+v, want bold red", a)
	}
	if a := s.CellAt(1, 0).A; a != (Attr{}) {
		t.Fatalf("cell1 attr = %+v, want default", a)
	}
}

func TestExtendedColor(t *testing.T) {
	s := New(10, 1)
	s.Write([]byte("\x1b[38;2;10;20;30mT"))
	if a := s.CellAt(0, 0).A; a.FG != "38;2;10;20;30" {
		t.Fatalf("fg = %q, want truecolor", a.FG)
	}
}

func TestResizePreserves(t *testing.T) {
	s := New(10, 3)
	s.Write([]byte("keep"))
	s.Resize(20, 5)
	if got := rowString(s, 0); got != "keep" {
		t.Fatalf("after resize row0 = %q, want keep", got)
	}
	if w, h := s.Size(); w != 20 || h != 5 {
		t.Fatalf("size = (%d,%d)", w, h)
	}
}

func TestPaintRoundsTrip(t *testing.T) {
	s := New(8, 2)
	s.Write([]byte("hi\r\nyo"))
	out := s.Paint()
	if !strings.Contains(out, "hi") || !strings.Contains(out, "yo") {
		t.Fatalf("paint missing content: %q", out)
	}
	// Feeding a Paint() of one screen into a fresh screen of the same size
	// should reproduce the visible text.
	s2 := New(8, 2)
	s2.Write([]byte(out))
	if rowString(s2, 0) != "hi" || rowString(s2, 1) != "yo" {
		t.Fatalf("repaint mismatch: row0=%q row1=%q", rowString(s2, 0), rowString(s2, 1))
	}
}

func TestUTF8BoxDrawing(t *testing.T) {
	s := New(10, 1)
	// "─│┼" are 3-byte UTF-8 each; feed raw bytes to ensure they're decoded.
	s.Write([]byte("─│┼"))
	if got := []rune(rowString(s, 0)); len(got) != 3 || got[0] != '─' || got[1] != '│' || got[2] != '┼' {
		t.Fatalf("row0 = %q (% x)", rowString(s, 0), rowString(s, 0))
	}
}

func TestUTF8SplitAcrossWrites(t *testing.T) {
	s := New(10, 1)
	box := []byte("─") // 0xE2 0x94 0x80
	s.Write(box[:1])
	s.Write(box[1:2])
	s.Write(box[2:])
	if got := rowString(s, 0); got != "─" {
		t.Fatalf("row0 = %q, want ─", got)
	}
}

func TestUTF8MultibyteThenAscii(t *testing.T) {
	s := New(10, 1)
	s.Write([]byte("café"))
	if got := rowString(s, 0); got != "café" {
		t.Fatalf("row0 = %q, want café", got)
	}
}

func TestOSCSwallowed(t *testing.T) {
	s := New(20, 1)
	s.Write([]byte("\x1b]0;window title\x07done"))
	if got := rowString(s, 0); got != "done" {
		t.Fatalf("row0 = %q, want done (OSC swallowed)", got)
	}
}
