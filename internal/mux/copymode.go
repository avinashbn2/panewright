package mux

import (
	"strconv"
	"strings"

	"panewright/internal/vt"
)

// copyState holds the per-pane copy-mode view: a scroll offset into the pane's
// scrollback, a cursor within the visible view, and an optional selection.
type copyState struct {
	off       int  // lines scrolled up from the live bottom (0 = bottom)
	cx, cy    int  // cursor within the visible view (0..w-1, 0..h-1)
	selecting bool // a selection is being made
	anchorAbs int  // selection anchor: absolute row
	anchorX   int  // selection anchor: column
}

// CopyAction is a navigation/command in copy mode, decoded by the input layer.
type CopyAction int

const (
	CopyUp CopyAction = iota
	CopyDown
	CopyLeft
	CopyRight
	CopyPageUp
	CopyPageDown
	CopyTop
	CopyBottom
	CopyLineStart
	CopyLineEnd
	CopyStartSel
	CopyCopy
	CopyCancel
)

// enterCopyLocked puts the active pane into copy mode at the live bottom.
func (m *Manager) enterCopyLocked() {
	p := m.activePaneLocked()
	if p == nil || p.copy != nil {
		return
	}
	cx, cy, _ := p.screen.Cursor()
	p.copy = &copyState{off: 0, cx: cx, cy: cy}
	m.comp.invalidate()
	m.dirty = true
}

// CopyActive reports whether the active pane is in copy mode.
func (m *Manager) CopyActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.activePaneLocked()
	return p != nil && p.copy != nil
}

// CopyDo applies a copy-mode action to the active pane.
func (m *Manager) CopyDo(a CopyAction) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.copyDoLocked(a)
}

func (m *Manager) copyDoLocked(a CopyAction) {
	p := m.activePaneLocked()
	if p == nil || p.copy == nil {
		return
	}
	c := p.copy
	w, h := p.screen.Size()
	total := p.screen.TotalRows()
	maxOff := total - h
	if maxOff < 0 {
		maxOff = 0
	}

	switch a {
	case CopyUp:
		if c.cy > 0 {
			c.cy--
		} else if c.off < maxOff {
			c.off++
		}
	case CopyDown:
		if c.cy < h-1 {
			c.cy++
		} else if c.off > 0 {
			c.off--
		}
	case CopyLeft:
		if c.cx > 0 {
			c.cx--
		}
	case CopyRight:
		if c.cx < w-1 {
			c.cx++
		}
	case CopyPageUp:
		c.off += h
		if c.off > maxOff {
			c.off = maxOff
		}
	case CopyPageDown:
		c.off -= h
		if c.off < 0 {
			c.off = 0
		}
	case CopyTop:
		c.off, c.cy = maxOff, 0
	case CopyBottom:
		c.off, c.cy = 0, h-1
	case CopyLineStart:
		c.cx = 0
	case CopyLineEnd:
		c.cx = lastNonBlank(p.screen.RowAt(c.off2abs(total, h, c.cy)), w)
	case CopyStartSel:
		c.selecting = true
		c.anchorAbs = c.off2abs(total, h, c.cy)
		c.anchorX = c.cx
	case CopyCopy:
		text := copySelection(p.screen, c, w, h)
		exitCopy(p)
		if text != "" {
			go setClipboard(text)
		}
	case CopyCancel:
		exitCopy(p)
	}
	m.comp.invalidate()
	m.dirty = true
}

func exitCopy(p *Pane) { p.copy = nil }

// off2abs maps a view row to an absolute screen row given the current offset.
func (c *copyState) off2abs(total, h, viewRow int) int {
	topAbs := total - h - c.off
	if topAbs < 0 {
		topAbs = 0
	}
	return topAbs + viewRow
}

// blitCopy renders the copy-mode view of pane p into the frame at rect r,
// applying the selection highlight and a position indicator.
func (m *Manager) blitCopy(f *Frame, p *Pane, r rect) {
	c := p.copy
	sw, sh := p.screen.Size()
	total := p.screen.TotalRows()
	topAbs := total - sh - c.off
	if topAbs < 0 {
		topAbs = 0
	}
	selLo, selHi, selActive := selectionRange(c, total, sh)

	for vy := 0; vy < r.h && vy < sh; vy++ {
		absY := topAbs + vy
		row := p.screen.RowAt(absY)
		for vx := 0; vx < r.w && vx < sw; vx++ {
			var cell vt.Cell
			if vx < len(row) {
				cell = row[vx]
			} else {
				cell = vt.Cell{R: ' '}
			}
			if selActive && inSelection(absY, vx, selLo, selHi) {
				cell.A.Reverse = !cell.A.Reverse
			}
			f.set(r.x+vx, r.y+vy, cell)
		}
	}

	// position indicator in the pane's top-right corner
	label := "[" + strconv.Itoa(c.off) + "/" + strconv.Itoa(total-sh) + "]"
	if c.off == 0 {
		label = "[COPY]"
	}
	if x := r.x + r.w - len(label); x >= r.x {
		putString(f, x, r.y, label, vt.Attr{Reverse: true})
	}
}

// pos is an absolute (row, col) selection endpoint.
type pos struct{ y, x int }

// selectionRange returns the ordered selection bounds (lo<=hi) and whether a
// selection is active.
func selectionRange(c *copyState, total, h int) (lo, hi pos, active bool) {
	if !c.selecting {
		return pos{}, pos{}, false
	}
	cur := pos{y: c.off2abs(total, h, c.cy), x: c.cx}
	anc := pos{y: c.anchorAbs, x: c.anchorX}
	if less(cur, anc) {
		return cur, anc, true
	}
	return anc, cur, true
}

func less(a, b pos) bool {
	if a.y != b.y {
		return a.y < b.y
	}
	return a.x < b.x
}

// inSelection reports whether absolute cell (y,x) lies within [lo,hi] using a
// line-oriented stream selection.
func inSelection(y, x int, lo, hi pos) bool {
	if y < lo.y || y > hi.y {
		return false
	}
	if y == lo.y && x < lo.x {
		return false
	}
	if y == hi.y && x > hi.x {
		return false
	}
	return true
}

// copySelection extracts the selected text as newline-separated lines with
// trailing blanks trimmed.
func copySelection(s *vt.Screen, c *copyState, w, h int) string {
	total := s.TotalRows()
	lo, hi, active := selectionRange(c, total, h)
	if !active {
		return ""
	}
	var b strings.Builder
	for y := lo.y; y <= hi.y; y++ {
		row := s.RowAt(y)
		x0, x1 := 0, w-1
		if y == lo.y {
			x0 = lo.x
		}
		if y == hi.y {
			x1 = hi.x
		}
		var line strings.Builder
		for x := x0; x <= x1 && x < len(row); x++ {
			r := row[x].R
			if r == 0 {
				r = ' '
			}
			line.WriteRune(r)
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		if y != hi.y {
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

// lastNonBlank returns the column of the last non-space cell in row (clamped to
// width w), or 0 if the row is blank.
func lastNonBlank(row []vt.Cell, w int) int {
	last := 0
	for x := 0; x < w && x < len(row); x++ {
		if row[x].R != ' ' && row[x].R != 0 {
			last = x
		}
	}
	return last
}
