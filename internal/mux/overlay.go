package mux

import (
	"sort"
	"strconv"

	"panewright/internal/config"
	"panewright/internal/vt"
)

// overlay is a modal list drawn over the window body (choose-window,
// list-keys). Items may carry an action run on Enter.
type overlay struct {
	title      string
	items      []overlayItem
	sel        int
	top        int // first visible item (scroll)
	selectable bool
}

type overlayItem struct {
	label  string
	action func(m *Manager) // run with lock held; nil = Enter just closes
}

// OverlayActive reports whether a modal list is capturing input.
func (m *Manager) OverlayActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.overlay != nil
}

// OverlayMove moves the selection by delta.
func (m *Manager) OverlayMove(delta int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.overlay
	if o == nil || !o.selectable {
		return
	}
	o.sel += delta
	if o.sel < 0 {
		o.sel = 0
	}
	if o.sel >= len(o.items) {
		o.sel = len(o.items) - 1
	}
	m.dirty = true
}

// OverlaySelect jumps the selection to index i (e.g. a typed digit) and
// commits it.
func (m *Manager) OverlaySelect(i int) {
	m.mu.Lock()
	o := m.overlay
	if o == nil || !o.selectable || i < 0 || i >= len(o.items) {
		m.mu.Unlock()
		return
	}
	o.sel = i
	m.mu.Unlock()
	m.OverlayCommit()
}

// OverlayCommit runs the selected item's action and closes the overlay.
func (m *Manager) OverlayCommit() {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.overlay
	if o == nil {
		return
	}
	m.overlay = nil
	m.comp.invalidate()
	m.dirty = true
	if o.sel < len(o.items) && o.items[o.sel].action != nil {
		o.items[o.sel].action(m)
	}
}

// OverlayCancel closes the overlay without acting.
func (m *Manager) OverlayCancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overlay = nil
	m.comp.invalidate()
	m.dirty = true
}

// openChooseWindowLocked opens the window chooser (prefix w).
func (m *Manager) openChooseWindowLocked() {
	items := make([]overlayItem, 0, len(m.windows))
	sel := 0
	for i, w := range m.windows {
		idx := i
		label := strconv.Itoa(w.num) + ": " + w.name + " (" + strconv.Itoa(len(w.panes())) + " panes)"
		if i == m.active {
			label += " (active)"
			sel = i
		}
		items = append(items, overlayItem{label: label, action: func(m *Manager) {
			m.activateWindowLocked(idx)
		}})
	}
	if len(items) == 0 {
		return
	}
	m.overlay = &overlay{title: "choose-window", items: items, sel: sel, selectable: true}
	m.comp.invalidate()
	m.dirty = true
}

// openListKeysLocked opens a read-only listing of the current key bindings.
func (m *Manager) openListKeysLocked() {
	var lines []string
	for k, cmds := range m.binds {
		lines = append(lines, "bind-key -T prefix "+padKey(k)+" "+cmdsString(cmds))
	}
	for k, cmds := range m.rootBinds {
		lines = append(lines, "bind-key -T root   "+padKey(k)+" "+cmdsString(cmds))
	}
	sort.Strings(lines)
	items := make([]overlayItem, len(lines))
	for i, l := range lines {
		items[i] = overlayItem{label: l}
	}
	m.overlay = &overlay{title: "list-keys (Esc to close)", items: items, selectable: true}
	m.comp.invalidate()
	m.dirty = true
}

func padKey(k config.Key) string {
	s := k.String()
	for len(s) < 12 {
		s += " "
	}
	return s
}

func cmdsString(cmds []config.Command) string {
	out := ""
	for i, c := range cmds {
		if i > 0 {
			out += " \\; "
		}
		out += c.Name
		for _, a := range c.Args {
			out += " " + a
		}
	}
	return out
}

// drawOverlay renders the modal list as a bordered box centered in the body.
func (m *Manager) drawOverlay(f *Frame) {
	o := m.overlay
	if o == nil {
		return
	}
	maxLabel := len(o.title)
	for _, it := range o.items {
		if n := len([]rune(it.label)); n > maxLabel {
			maxLabel = n
		}
	}
	bw := maxLabel + 4
	if bw > m.cols-2 {
		bw = m.cols - 2
	}
	if bw < 20 {
		bw = 20
	}
	bodyTop := m.bodyTop()
	maxRows := m.bodyRows() - 2
	bh := len(o.items) + 2
	if bh > maxRows {
		bh = maxRows
	}
	if bh < 3 {
		bh = 3
	}
	x0 := (m.cols - bw) / 2
	y0 := bodyTop + (m.bodyRows()-bh)/2
	if x0 < 0 {
		x0 = 0
	}
	if y0 < bodyTop {
		y0 = bodyTop
	}

	visible := bh - 2
	if o.sel < o.top {
		o.top = o.sel
	}
	if o.sel >= o.top+visible {
		o.top = o.sel - visible + 1
	}

	frameAtt := vt.Attr{FG: "33"} // yellow border, like tmux menus
	// top border with title
	f.set(x0, y0, vt.Cell{R: '┌', A: frameAtt})
	f.set(x0+bw-1, y0, vt.Cell{R: '┐', A: frameAtt})
	for x := x0 + 1; x < x0+bw-1; x++ {
		f.set(x, y0, vt.Cell{R: '─', A: frameAtt})
	}
	putString(f, x0+2, y0, " "+o.title+" ", frameAtt)
	// rows
	for i := 0; i < visible; i++ {
		y := y0 + 1 + i
		f.set(x0, y, vt.Cell{R: '│', A: frameAtt})
		f.set(x0+bw-1, y, vt.Cell{R: '│', A: frameAtt})
		att := vt.Attr{}
		label := ""
		idx := o.top + i
		if idx < len(o.items) {
			label = o.items[idx].label
			if o.selectable && idx == o.sel {
				att = vt.Attr{Reverse: true}
			}
		}
		rl := []rune(label)
		for x := 0; x < bw-2; x++ {
			r := ' '
			if x >= 1 && x-1 < len(rl) {
				r = rl[x-1]
			}
			f.set(x0+1+x, y, vt.Cell{R: r, A: att})
		}
	}
	// bottom border
	y1 := y0 + bh - 1
	f.set(x0, y1, vt.Cell{R: '└', A: frameAtt})
	f.set(x0+bw-1, y1, vt.Cell{R: '┘', A: frameAtt})
	for x := x0 + 1; x < x0+bw-1; x++ {
		f.set(x, y1, vt.Cell{R: '─', A: frameAtt})
	}
}
