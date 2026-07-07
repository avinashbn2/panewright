package mux

// Mouse handling: left-click selects the pane under the cursor; pressing on a
// divider and dragging resizes the split. Coordinates are 0-based host cells.

// MouseEnabled reports whether mouse reporting should be active.
func (m *Manager) MouseEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mouseOn
}

// inBody reports whether host row y lies within the window body (not the
// status bar).
func (m *Manager) inBody(y int) bool {
	return y >= m.bodyTop() && y < m.bodyTop()+m.bodyRows()
}

// MouseDown handles a left-button press.
func (m *Manager) MouseDown(x, y int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.mouseOn || !m.inBody(y) {
		return
	}
	if pane, div, isDiv := m.hitTestLocked(x, y); isDiv {
		m.dragging = true
		m.dragNode = div.owner
		m.dragVertical = div.vertical
		m.dragArea = div.area
	} else if pane != nil {
		if w := m.activeWindowLocked(); w != nil {
			m.setActivePaneLocked(w, pane)
		}
	}
}

// MouseDrag handles motion while the left button is held.
func (m *Manager) MouseDrag(x, y int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.dragging || m.dragNode == nil {
		return
	}
	a := m.dragArea
	var ratio float64
	if m.dragVertical {
		if a.w <= 1 {
			return
		}
		ratio = float64(x-a.x) / float64(a.w-1)
	} else {
		if a.h <= 1 {
			return
		}
		ratio = float64(y-a.y) / float64(a.h-1)
	}
	if ratio < 0.05 {
		ratio = 0.05
	}
	if ratio > 0.95 {
		ratio = 0.95
	}
	m.dragNode.ratio = ratio
	if w := m.activeWindowLocked(); w != nil {
		m.relayoutLocked(w)
	}
	m.comp.invalidate()
	m.dirty = true
}

// MouseWheel scrolls the pane under the cursor: wheeling up enters copy mode
// (if needed) and scrolls back through history; wheeling down scrolls toward
// the live bottom and leaves copy mode at the bottom.
func (m *Manager) MouseWheel(up bool, x, y int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.mouseOn || !m.inBody(y) {
		return
	}
	pane, _, isDiv := m.hitTestLocked(x, y)
	if isDiv || pane == nil {
		return
	}
	if pane.copy == nil {
		if !up {
			return // nothing to do; already at the live bottom
		}
		cx, cy, _ := pane.screen.Cursor()
		pane.copy = &copyState{off: 0, cx: cx, cy: cy}
		if w := m.activeWindowLocked(); w != nil {
			w.active = pane
		}
	}
	c := pane.copy
	_, h := pane.screen.Size()
	maxOff := pane.screen.TotalRows() - h
	if maxOff < 0 {
		maxOff = 0
	}
	const step = 3
	if up {
		c.off += step
		if c.off > maxOff {
			c.off = maxOff
		}
	} else {
		c.off -= step
		if c.off <= 0 {
			pane.copy = nil // back at the bottom: exit copy mode
		}
	}
	m.comp.invalidate()
	m.dirty = true
}

// MouseUp ends any divider drag.
func (m *Manager) MouseUp() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dragging = false
	m.dragNode = nil
}

// hitTestLocked finds what's at (x,y) in the active window: a divider takes
// priority over the pane it sits between.
func (m *Manager) hitTestLocked(x, y int) (pane *Pane, div divider, isDiv bool) {
	w := m.activeWindowLocked()
	if w == nil {
		return nil, divider{}, false
	}
	var prs []paneRect
	var divs []divider
	w.root.walk(rect{0, m.bodyTop(), m.cols, m.bodyRows()}, &prs, &divs)
	for _, d := range divs {
		if d.vertical {
			if x == d.x && y >= d.y && y < d.y+d.length {
				return nil, d, true
			}
		} else {
			if y == d.y && x >= d.x && x < d.x+d.length {
				return nil, d, true
			}
		}
	}
	for _, pr := range prs {
		if x >= pr.r.x && x < pr.r.x+pr.r.w && y >= pr.r.y && y < pr.r.y+pr.r.h {
			return pr.pane, divider{}, false
		}
	}
	return nil, divider{}, false
}
