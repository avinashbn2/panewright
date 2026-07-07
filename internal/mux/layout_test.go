package mux

import "testing"

func leafNode(p *Pane) *node { return &node{dir: leaf, pane: p} }

func rectsFor(root *node, w, h int) map[*Pane]rect {
	var prs []paneRect
	var divs []divider
	root.walk(rect{0, 0, w, h}, &prs, &divs)
	out := map[*Pane]rect{}
	for _, pr := range prs {
		out[pr.pane] = pr.r
	}
	return out
}

func TestSingleLeafFillsArea(t *testing.T) {
	p := &Pane{num: 0}
	root := leafNode(p)
	r := rectsFor(root, 80, 24)
	if r[p] != (rect{0, 0, 80, 24}) {
		t.Fatalf("leaf rect = %+v", r[p])
	}
}

func TestVerticalSplitSideBySide(t *testing.T) {
	a, b := &Pane{num: 0}, &Pane{num: 1}
	root := leafNode(a)
	if !root.splitLeaf(a, splitV, b) {
		t.Fatal("splitLeaf failed")
	}
	r := rectsFor(root, 81, 24) // 81 = 40 + 1 divider + 40
	ra, rb := r[a], r[b]
	if ra.h != 24 || rb.h != 24 {
		t.Fatalf("heights wrong: %+v %+v", ra, rb)
	}
	if ra.x != 0 || rb.x != ra.w+1 {
		t.Fatalf("not side by side: %+v %+v", ra, rb)
	}
	if ra.w+rb.w+1 != 81 {
		t.Fatalf("widths don't account for divider: %d+%d+1 != 81", ra.w, rb.w)
	}
}

func TestHorizontalSplitStacked(t *testing.T) {
	a, b := &Pane{num: 0}, &Pane{num: 1}
	root := leafNode(a)
	root.splitLeaf(a, splitH, b)
	r := rectsFor(root, 80, 25) // 25 = 12 + 1 + 12
	ra, rb := r[a], r[b]
	if ra.w != 80 || rb.w != 80 {
		t.Fatalf("widths wrong: %+v %+v", ra, rb)
	}
	if ra.y != 0 || rb.y != ra.h+1 {
		t.Fatalf("not stacked: %+v %+v", ra, rb)
	}
}

func TestRemoveCollapsesToSibling(t *testing.T) {
	a, b := &Pane{num: 0}, &Pane{num: 1}
	root := leafNode(a)
	root.splitLeaf(a, splitV, b)
	newRoot, ok := removeFromTree(root, a)
	if !ok {
		t.Fatal("remove failed")
	}
	if newRoot.dir != leaf || newRoot.pane != b {
		t.Fatalf("expected sibling b as new root, got %+v", newRoot)
	}
}

func TestRemoveLastReturnsNil(t *testing.T) {
	a := &Pane{num: 0}
	root := leafNode(a)
	newRoot, ok := removeFromTree(root, a)
	if !ok || newRoot != nil {
		t.Fatalf("expected nil root, ok; got %v %v", newRoot, ok)
	}
}

func TestCollectPanesOrder(t *testing.T) {
	a, b, c := &Pane{num: 0}, &Pane{num: 1}, &Pane{num: 2}
	root := leafNode(a)
	root.splitLeaf(a, splitV, b) // a | b
	root.splitLeaf(b, splitH, c) // a | (b / c)
	got := collectPanes(root)
	if len(got) != 3 || got[0] != a || got[1] != b || got[2] != c {
		t.Fatalf("order = %v", got)
	}
}

func TestNarrowSplitDoesNotPanic(t *testing.T) {
	a, b := &Pane{num: 0}, &Pane{num: 1}
	root := leafNode(a)
	root.splitLeaf(a, splitV, b)
	_ = rectsFor(root, 2, 5) // too narrow for a real divider
}
