package mux

import (
	"strings"

	"panewright/internal/conpty"
	"panewright/internal/vt"
)

// Pane is a single shell hosted in a ConPTY with its own screen model. Panes
// are tiled within a Window by the layout tree.
type Pane struct {
	num     int
	win     *Window
	pty     *conpty.ConPTY
	proc    *conpty.Process
	screen  *vt.Screen
	cmdName string     // basename of the hosted command, for #{pane_title}
	w, h    int        // current size, to avoid redundant resizes
	copy    *copyState // non-nil when this pane is in copy mode
}

// title is the pane's display title: the OSC 0/2 title if the application set
// one, else the hosted command's name.
func (p *Pane) title() string {
	if t := p.screen.Title(); t != "" {
		return t
	}
	return p.cmdName
}

// cmdBase extracts a display name from a command line: the basename of the
// first word, without a .exe suffix.
func cmdBase(cmdline string) string {
	f := strings.Fields(cmdline)
	if len(f) == 0 {
		return ""
	}
	name := f[0]
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSuffix(strings.ToLower(name), ".exe")
}

// Window is a collection of panes arranged by a layout tree, with one active.
// zoomed temporarily promotes the active pane to fill the whole window body.
type Window struct {
	num       int
	name      string
	root      *node
	active    *Pane
	lastPane  *Pane // previously active pane (last-pane)
	zoomed    bool
	layoutIdx int // current position in the next-layout cycle
}

// panes returns the window's panes in layout order.
func (w *Window) panes() []*Pane { return collectPanes(w.root) }
