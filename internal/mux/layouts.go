package mux

// Named layouts (select-layout / next-layout): each rebuilds a window's layout
// tree from its current pane list.

var layoutNames = []string{"even-horizontal", "even-vertical", "main-vertical", "main-horizontal", "tiled"}

// applyLayoutLocked rebuilds w's tree in the named arrangement. Returns false
// for an unknown layout name.
func (m *Manager) applyLayoutLocked(w *Window, name string) bool {
	if w == nil {
		return false
	}
	panes := w.panes()
	if len(panes) == 0 {
		return false
	}
	var root *node
	switch name {
	case "even-horizontal":
		root = chain(panes, splitV)
	case "even-vertical":
		root = chain(panes, splitH)
	case "main-vertical":
		root = mainLayout(panes, splitV)
	case "main-horizontal":
		root = mainLayout(panes, splitH)
	case "tiled":
		root = tiled(panes)
	default:
		return false
	}
	if w.zoomed {
		w.zoomed = false
	}
	w.root = root
	m.relayoutLocked(w)
	m.comp.invalidate()
	m.dirty = true
	return true
}

// nextLayoutLocked cycles w through the named layouts.
func (m *Manager) nextLayoutLocked(w *Window) {
	if w == nil {
		return
	}
	w.layoutIdx = (w.layoutIdx + 1) % len(layoutNames)
	m.applyLayoutLocked(w, layoutNames[w.layoutIdx])
}

// chain builds an even split of panes along dir: a right-leaning chain where
// each node gives its first child 1/k of the remaining space.
func chain(panes []*Pane, dir splitDir) *node {
	if len(panes) == 1 {
		return &node{dir: leaf, pane: panes[0]}
	}
	return &node{
		dir:   dir,
		ratio: 1.0 / float64(len(panes)),
		a:     &node{dir: leaf, pane: panes[0]},
		b:     chain(panes[1:], dir),
	}
}

// mainLayout gives the first pane ~60% of the space and stacks the rest evenly
// in the other direction.
func mainLayout(panes []*Pane, dir splitDir) *node {
	if len(panes) == 1 {
		return &node{dir: leaf, pane: panes[0]}
	}
	other := splitH
	if dir == splitH {
		other = splitV
	}
	return &node{
		dir:   dir,
		ratio: 0.6,
		a:     &node{dir: leaf, pane: panes[0]},
		b:     chain(panes[1:], other),
	}
}

// tiled arranges panes in a near-square grid: rows stacked vertically, each
// row an even horizontal chain.
func tiled(panes []*Pane) *node {
	n := len(panes)
	cols := 1
	for cols*cols < n {
		cols++
	}
	var rows []*node
	for i := 0; i < n; i += cols {
		j := i + cols
		if j > n {
			j = n
		}
		rows = append(rows, chain(panes[i:j], splitV))
	}
	return chainNodes(rows, splitH)
}

// chainNodes evenly chains prebuilt subtrees along dir.
func chainNodes(nodes []*node, dir splitDir) *node {
	if len(nodes) == 1 {
		return nodes[0]
	}
	return &node{
		dir:   dir,
		ratio: 1.0 / float64(len(nodes)),
		a:     nodes[0],
		b:     chainNodes(nodes[1:], dir),
	}
}

// swapPaneLocked swaps the active pane with the previous (-1) or next (+1)
// pane in layout order, keeping the active pane selected in its new position.
func (m *Manager) swapPaneLocked(delta int) {
	w := m.activeWindowLocked()
	if w == nil || w.active == nil {
		return
	}
	panes := w.panes()
	if len(panes) < 2 {
		return
	}
	cur := -1
	for i, p := range panes {
		if p == w.active {
			cur = i
			break
		}
	}
	if cur < 0 {
		return
	}
	other := (cur + delta + len(panes)) % len(panes)
	swapLeafPanes(w.root, panes[cur], panes[other])
	m.relayoutLocked(w)
	m.comp.invalidate()
	m.dirty = true
}

// rotateWindowLocked moves every pane to the next leaf position.
func (m *Manager) rotateWindowLocked() {
	w := m.activeWindowLocked()
	if w == nil {
		return
	}
	panes := w.panes()
	if len(panes) < 2 {
		return
	}
	rotated := append([]*Pane{panes[len(panes)-1]}, panes[:len(panes)-1]...)
	i := 0
	var rec func(*node)
	rec = func(x *node) {
		if x == nil {
			return
		}
		if x.dir == leaf {
			x.pane = rotated[i]
			i++
			return
		}
		rec(x.a)
		rec(x.b)
	}
	rec(w.root)
	m.relayoutLocked(w)
	m.comp.invalidate()
	m.dirty = true
}

// swapLeafPanes exchanges the panes held by the leaves containing a and b.
func swapLeafPanes(n *node, a, b *Pane) {
	if n == nil {
		return
	}
	if n.dir == leaf {
		if n.pane == a {
			n.pane = b
		} else if n.pane == b {
			n.pane = a
		}
		return
	}
	swapLeafPanes(n.a, a, b)
	swapLeafPanes(n.b, a, b)
}

// breakPaneLocked moves the active pane out of its window into a new window.
func (m *Manager) breakPaneLocked() {
	w := m.activeWindowLocked()
	if w == nil || w.active == nil || len(w.panes()) < 2 {
		return
	}
	p := w.active
	newRoot, ok := removeFromTree(w.root, p)
	if !ok {
		return
	}
	if w.zoomed {
		w.zoomed = false
	}
	w.root = newRoot
	w.active = collectPanes(w.root)[0]

	nw := &Window{num: m.nextWin, name: p.title(), root: &node{dir: leaf, pane: p}, active: p}
	if nw.name == "" {
		nw.name = "shell"
	}
	p.win = nw
	m.nextWin++
	m.windows = append(m.windows, nw)
	m.activateWindowLocked(len(m.windows) - 1)
	m.relayoutLocked(w)
	m.relayoutLocked(nw)
	m.comp.invalidate()
	m.dirty = true
}
