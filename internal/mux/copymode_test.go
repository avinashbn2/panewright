package mux

import (
	"testing"

	"panewright/internal/vt"
)

func TestCopySelectionSingleLine(t *testing.T) {
	s := vt.New(20, 3)
	s.Write([]byte("hello world"))
	// select columns 0..4 on row 0 ("hello")
	c := &copyState{selecting: true, anchorAbs: 0, anchorX: 0, cx: 4, cy: 0}
	if got := copySelection(s, c, 20, 3); got != "hello" {
		t.Fatalf("selection = %q, want %q", got, "hello")
	}
}

func TestCopySelectionMultiLine(t *testing.T) {
	s := vt.New(20, 3)
	s.Write([]byte("abc\r\ndef\r\nghi"))
	// from row0 col1 to row2 col1 -> "bc\r\ndef\r\ngh"
	c := &copyState{selecting: true, anchorAbs: 0, anchorX: 1, cx: 1, cy: 2}
	want := "bc\r\ndef\r\ngh"
	if got := copySelection(s, c, 20, 3); got != want {
		t.Fatalf("selection = %q, want %q", got, want)
	}
}

func TestCopySelectionAnchorAfterCursor(t *testing.T) {
	// anchor below/after the cursor should still yield ordered text.
	s := vt.New(20, 2)
	s.Write([]byte("xy\r\nzw"))
	c := &copyState{selecting: true, anchorAbs: 1, anchorX: 1, cx: 0, cy: 0}
	want := "xy\r\nzw"
	if got := copySelection(s, c, 20, 2); got != want {
		t.Fatalf("selection = %q, want %q", got, want)
	}
}

func TestSetRatioCellsClamps(t *testing.T) {
	n := &node{dir: splitV, ratio: 0.5}
	d := divider{owner: n, area: rect{0, 0, 10, 5}}
	setRatioCells(d, 100, 10) // way past the right edge
	// boundary clamped to total-2 = 8, ratio = 8/9
	if n.ratio < 0.85 || n.ratio > 0.95 {
		t.Fatalf("ratio = %v, want ~0.889", n.ratio)
	}
	setRatioCells(d, -5, 10) // past the left edge -> boundary 1
	if n.ratio > 0.2 {
		t.Fatalf("ratio = %v, want ~0.111", n.ratio)
	}
}
