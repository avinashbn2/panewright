package mux

import "math"

// The layout of a window is a binary tree. Leaves hold panes; internal nodes
// split their area either vertically (a vertical divider, panes side by side)
// or horizontally (a horizontal divider, panes stacked). ratio is the fraction
// of the splittable space given to child a (the first child).

type splitDir int

const (
	leaf splitDir = iota
	splitV         // vertical divider | : a is left, b is right
	splitH         // horizontal divider — : a is top, b is bottom
)

type node struct {
	dir   splitDir
	pane  *Pane // leaf only
	ratio float64
	a, b  *node
}

type rect struct{ x, y, w, h int }

type paneRect struct {
	pane *Pane
	r    rect
}

// divider is a drawn separator line between two child areas. owner/area let the
// mouse layer map a divider back to its split node for drag-to-resize.
type divider struct {
	vertical bool
	x, y     int // top-left of the line (window-body coordinates)
	length   int
	owner    *node
	area     rect
}

// walk assigns rectangles to every leaf within area r, collecting pane
// rectangles and divider lines.
func (n *node) walk(r rect, panes *[]paneRect, divs *[]divider) {
	if n == nil {
		return
	}
	if n.dir == leaf {
		*panes = append(*panes, paneRect{pane: n.pane, r: r})
		return
	}
	if n.dir == splitV {
		if r.w < 3 { // too narrow to split; collapse onto a
			n.a.walk(r, panes, divs)
			return
		}
		aw := int(math.Round(float64(r.w-1) * n.ratio))
		if aw < 1 {
			aw = 1
		}
		if aw > r.w-2 {
			aw = r.w - 2
		}
		bw := r.w - 1 - aw
		*divs = append(*divs, divider{vertical: true, x: r.x + aw, y: r.y, length: r.h, owner: n, area: r})
		n.a.walk(rect{r.x, r.y, aw, r.h}, panes, divs)
		n.b.walk(rect{r.x + aw + 1, r.y, bw, r.h}, panes, divs)
		return
	}
	// splitH
	if r.h < 3 {
		n.a.walk(r, panes, divs)
		return
	}
	ah := int(math.Round(float64(r.h-1) * n.ratio))
	if ah < 1 {
		ah = 1
	}
	if ah > r.h-2 {
		ah = r.h - 2
	}
	bh := r.h - 1 - ah
	*divs = append(*divs, divider{vertical: false, x: r.x, y: r.y + ah, length: r.w, owner: n, area: r})
	n.a.walk(rect{r.x, r.y, r.w, ah}, panes, divs)
	n.b.walk(rect{r.x, r.y + ah + 1, r.w, bh}, panes, divs)
}

// splitLeaf converts the leaf holding target into a split node, keeping target
// as child a and np as child b. Returns false if target isn't found.
func (n *node) splitLeaf(target *Pane, dir splitDir, np *Pane) bool {
	if n == nil {
		return false
	}
	if n.dir == leaf {
		if n.pane != target {
			return false
		}
		n.a = &node{dir: leaf, pane: target}
		n.b = &node{dir: leaf, pane: np}
		n.dir = dir
		n.ratio = 0.5
		n.pane = nil
		return true
	}
	return n.a.splitLeaf(target, dir, np) || n.b.splitLeaf(target, dir, np)
}

// removeFromTree removes the leaf holding target, collapsing its parent split
// into the surviving sibling. It returns the replacement subtree (possibly nil
// if the whole subtree is gone) and whether target was found.
func removeFromTree(n *node, target *Pane) (*node, bool) {
	if n == nil {
		return nil, false
	}
	if n.dir == leaf {
		if n.pane == target {
			return nil, true
		}
		return n, false
	}
	if na, ok := removeFromTree(n.a, target); ok {
		if na == nil {
			return n.b, true
		}
		n.a = na
		return n, true
	}
	if nb, ok := removeFromTree(n.b, target); ok {
		if nb == nil {
			return n.a, true
		}
		n.b = nb
		return n, true
	}
	return n, false
}

// collectPanes returns the window's panes in left-to-right, top-to-bottom order.
func collectPanes(n *node) []*Pane {
	var out []*Pane
	var rec func(*node)
	rec = func(x *node) {
		if x == nil {
			return
		}
		if x.dir == leaf {
			out = append(out, x.pane)
			return
		}
		rec(x.a)
		rec(x.b)
	}
	rec(n)
	return out
}
