package mux

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"panewright/internal/conpty"
	"panewright/internal/config"
	"panewright/internal/vt"
)

// Manager is panewright's control plane: it owns windows (each a tree of panes),
// tracks the active window/pane, composites the active window plus a status bar
// onto the host, and applies configuration (prefix, bindings, options).
type Manager struct {
	mu      sync.Mutex
	out     io.Writer
	comp    *compositor
	cols    int
	rows    int
	shell   string
	session string

	windows []*Window
	active  int
	lastWin *Window // previously active window (last-window)
	nextWin int
	nextNum int // pane numbering

	// configuration
	prefix    config.Key
	binds     map[config.Key][]config.Command
	rootBinds map[config.Key][]config.Command
	opts      map[string]string // merged raw options
	baseIndex int
	paneBase  int
	mouseOn   bool

	// derived appearance / behavior (recomputed from opts)
	statusOn        bool
	statusTop       bool
	statusJustify   string
	statusAtt       vt.Attr
	msgAtt          vt.Attr
	borderAtt       vt.Attr
	borderActiveAtt vt.Attr
	statusLeft      string
	statusRight     string
	statusLeftLen   int
	statusRightLen  int
	winFmt          string
	winCurFmt       string
	winSep          string
	winAtt          vt.Attr
	winCurAtt       vt.Attr
	displayTime     time.Duration
	panesTime       time.Duration
	historyLimit    int
	renumber        bool

	// divider drag state
	dragging     bool
	dragNode     *node
	dragVertical bool
	dragArea     rect

	// modal state
	prompt     *prompt   // status-line prompt; nil when inactive
	overlay    *overlay  // modal list; nil when inactive
	msg        string    // transient status-line message
	msgUntil   time.Time // when msg expires
	panesUntil time.Time // display-panes overlay deadline

	// detachHook, when set (server mode), is invoked by detach-client instead
	// of tearing the manager down, so the shells survive a client detaching.
	detachHook func()

	dirty  bool
	done   chan struct{}
	closed bool
}

// NewManager creates a manager and applies cfg, then starts the render loop.
func NewManager(out io.Writer, cols, rows int, shell string, cfg *config.Config) *Manager {
	m := &Manager{
		out:       out,
		comp:      newCompositor(out),
		cols:      cols,
		rows:      rows,
		shell:     shell,
		session:   "main",
		active:    -1,
		prefix:    config.Key{Rune: 'b', Ctrl: true},
		binds:     defaultBinds(),
		rootBinds: map[config.Key][]config.Command{},
		opts:      map[string]string{},
		mouseOn:   true, // on by default; disable with `set -g mouse off`
		done:      make(chan struct{}),
	}
	m.reapplyOptionsLocked()
	if cfg != nil {
		m.applyConfigLocked(cfg)
	}
	go m.renderLoop()
	go m.clockLoop()
	return m
}

// SetSession names the session for #{session_name} expansion.
func (m *Manager) SetSession(name string) {
	m.mu.Lock()
	m.session = name
	m.mu.Unlock()
}

// defaultBinds is the built-in prefix key table (tmux defaults where
// implemented).
func defaultBinds() map[config.Key][]config.Command {
	b := map[config.Key][]config.Command{}
	add := func(r rune, name string, args ...string) {
		b[config.Key{Rune: r}] = []config.Command{{Name: name, Args: args}}
	}
	add('c', "new-window")
	add('n', "next-window")
	add('p', "previous-window")
	add('l', "last-window")
	add(';', "last-pane")
	add('x', "confirm-before", "-p", "kill-pane? (y/n)", "kill-pane")
	add('&', "confirm-before", "-p", "kill-window? (y/n)", "kill-window")
	add('d', "detach-client")
	add('o', "select-pane", "-t", ":.+")
	add('"', "split-window", "-v")
	add('%', "split-window", "-h")
	add('h', "split-window", "-v") // stacked (top/bottom)
	add('v', "split-window", "-h") // side by side (left/right)
	add('z', "resize-pane", "-Z")  // zoom toggle
	add(',', "rename-window")
	add('[', "copy-mode")
	add(']', "paste-buffer")
	add('{', "swap-pane", "-U")
	add('}', "swap-pane", "-D")
	add('!', "break-pane")
	add('w', "choose-window")
	add('?', "list-keys")
	add(':', "command-prompt")
	add('q', "display-panes")
	add('t', "clock-mode")
	add(' ', "next-layout")
	add(config.KeyPgUp, "copy-mode", "-u")
	b[config.Key{Rune: 'b', Ctrl: true}] = []config.Command{{Name: "send-prefix"}}
	b[config.Key{Rune: 'o', Ctrl: true}] = []config.Command{{Name: "rotate-window"}}
	// directional pane selection with prefix + arrows
	add(config.KeyLeft, "select-pane", "-L")
	add(config.KeyRight, "select-pane", "-R")
	add(config.KeyUp, "select-pane", "-U")
	add(config.KeyDown, "select-pane", "-D")
	// pane resize with prefix + Ctrl-arrows
	bind := func(r rune, ctrl bool, name string, args ...string) {
		b[config.Key{Rune: r, Ctrl: ctrl}] = []config.Command{{Name: name, Args: args}}
	}
	bind(config.KeyLeft, true, "resize-pane", "-L")
	bind(config.KeyRight, true, "resize-pane", "-R")
	bind(config.KeyUp, true, "resize-pane", "-U")
	bind(config.KeyDown, true, "resize-pane", "-D")
	return b
}

// applyConfigLocked merges a parsed config: prefix, bindings and options.
func (m *Manager) applyConfigLocked(cfg *config.Config) {
	if cfg.HasPrefix {
		delete(m.binds, m.prefix)
		m.prefix = cfg.Prefix
		// keep send-prefix bound to the (new) prefix key too
		m.binds[m.prefix] = []config.Command{{Name: "send-prefix"}}
	}
	for _, u := range cfg.Unbinds {
		if u.Root {
			delete(m.rootBinds, u.Key)
		} else {
			delete(m.binds, u.Key)
		}
	}
	for _, bd := range cfg.Binds {
		if bd.Root {
			m.rootBinds[bd.Key] = bd.Cmds
		} else {
			m.binds[bd.Key] = bd.Cmds
		}
	}
	for k, v := range cfg.Options {
		m.opts[k] = v
	}
	m.reapplyOptionsLocked()
	m.comp.invalidate()
	m.dirty = true
}

// optDefault returns the option's value, or def when unset/empty-key.
func (m *Manager) optDefault(name, def string) string {
	if v, ok := m.opts[name]; ok {
		return v
	}
	return def
}

// reapplyOptionsLocked recomputes all derived settings from m.opts.
func (m *Manager) reapplyOptionsLocked() {
	m.statusOn = true
	if v, ok := m.opts["status"]; ok {
		m.statusOn = v != "off" && v != "0"
	}
	m.statusTop = m.optDefault("status-position", "bottom") == "top"
	m.statusJustify = m.optDefault("status-justify", "left")
	if v, ok := m.opts["mouse"]; ok {
		m.mouseOn = v == "on" || v == "1"
	}
	m.baseIndex = atoiDefault(m.opts["base-index"], 0)
	m.paneBase = atoiDefault(m.opts["pane-base-index"], 0)
	if m.nextWin < m.baseIndex && len(m.windows) == 0 {
		m.nextWin = m.baseIndex
	}
	if m.nextNum < m.paneBase {
		m.nextNum = m.paneBase
	}

	m.statusAtt = vt.Attr{Reverse: true}
	if att, ok := buildStatusAttr(m.opts); ok {
		m.statusAtt = att
	}
	m.msgAtt = vt.Attr{FG: "30", BG: "43"} // tmux default: black on yellow
	if s, ok := m.opts["message-style"]; ok {
		m.msgAtt = vt.Attr{}
		applyStyle(&m.msgAtt, s)
	}
	m.borderAtt = vt.Attr{}
	if s, ok := m.opts["pane-border-style"]; ok {
		applyStyle(&m.borderAtt, s)
	}
	m.borderActiveAtt = vt.Attr{FG: "32"} // green, like tmux
	if s, ok := m.opts["pane-active-border-style"]; ok {
		m.borderActiveAtt = vt.Attr{}
		applyStyle(&m.borderActiveAtt, s)
	}

	m.statusLeft = m.optDefault("status-left", "[#S] ")
	m.statusRight = m.optDefault("status-right", " %H:%M %d-%b-%y")
	m.statusLeftLen = atoiDefault(m.opts["status-left-length"], 10)
	m.statusRightLen = atoiDefault(m.opts["status-right-length"], 40)
	m.winFmt = m.optDefault("window-status-format", "#I:#W#F")
	m.winCurFmt = m.optDefault("window-status-current-format", "#I:#W#F")
	m.winSep = m.optDefault("window-status-separator", " ")

	m.winAtt = m.statusAtt
	if s, ok := m.opts["window-status-style"]; ok {
		applyStyleWithBase(&m.winAtt, s, m.statusAtt)
	}
	m.winCurAtt = m.statusAtt
	m.winCurAtt.Bold = true
	m.winCurAtt.Reverse = !m.winCurAtt.Reverse
	if s, ok := m.opts["window-status-current-style"]; ok {
		m.winCurAtt = m.statusAtt
		applyStyleWithBase(&m.winCurAtt, s, m.statusAtt)
	}

	m.displayTime = time.Duration(atoiDefault(m.opts["display-time"], 750)) * time.Millisecond
	m.panesTime = time.Duration(atoiDefault(m.opts["display-panes-time"], 1000)) * time.Millisecond
	m.historyLimit = atoiDefault(m.opts["history-limit"], 2000)
	m.renumber = m.opts["renumber-windows"] == "on"
}

func (m *Manager) Done() <-chan struct{} { return m.done }

// clockLoop nudges a redraw each second so the status-bar clock advances.
func (m *Manager) clockLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
		if m.statusOn {
			m.dirty = true
		}
		m.mu.Unlock()
	}
}

func (m *Manager) bodyRows() int {
	r := m.rows
	if m.statusOn {
		r--
	}
	if r < 1 {
		r = 1
	}
	return r
}

// bodyTop is the first row of the window body (1 when the status bar is at the
// top of the screen).
func (m *Manager) bodyTop() int {
	if m.statusOn && m.statusTop {
		return 1
	}
	return 0
}

// statusRow is the host row the status bar (and prompt/message) occupies.
func (m *Manager) statusRow() int {
	if m.statusTop {
		return 0
	}
	return m.rows - 1
}

// ---- lifecycle / window & pane creation ----

// NewWindow creates a new window with a single pane and activates it.
func (m *Manager) NewWindow() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.newWindowLocked()
}

func (m *Manager) newWindowLocked() error {
	p, err := m.newPaneLocked(m.cols, m.bodyRows())
	if err != nil {
		return err
	}
	name := p.cmdName
	if name == "" {
		name = "shell"
	}
	w := &Window{num: m.nextWin, name: name, root: &node{dir: leaf, pane: p}, active: p}
	p.win = w
	m.nextWin++
	m.windows = append(m.windows, w)
	m.activateWindowLocked(len(m.windows) - 1)
	return nil
}

// paneCommand picks the command a new pane hosts: default-command wins when
// set (the server strips it if an explicit command was given on the CLI).
func (m *Manager) paneCommand() string {
	if c := strings.TrimSpace(m.opts["default-command"]); c != "" {
		return c
	}
	if c := strings.TrimSpace(m.opts["default-shell"]); c != "" {
		return c
	}
	return m.shell
}

func (m *Manager) newPaneLocked(w, h int) (*Pane, error) {
	pty, err := conpty.New(int16(w), int16(h))
	if err != nil {
		return nil, err
	}
	cmdline := m.paneCommand()
	proc, err := pty.Spawn(cmdline)
	if err != nil {
		pty.Close()
		return nil, err
	}
	p := &Pane{
		num:     m.nextNum,
		pty:     pty,
		proc:    proc,
		screen:  vt.New(w, h),
		cmdName: cmdBase(cmdline),
		w:       w,
		h:       h,
	}
	p.screen.SetMaxScroll(m.historyLimit)
	m.nextNum++
	go m.readLoop(p)
	go m.waitLoop(p)
	return p, nil
}

func (m *Manager) readLoop(p *Pane) {
	buf := make([]byte, 8192)
	for {
		n, err := p.pty.Out.Read(buf)
		if n > 0 {
			m.mu.Lock()
			p.screen.Write(buf[:n])
			m.dirty = true
			m.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (m *Manager) waitLoop(p *Pane) {
	code, err := p.proc.Wait()
	dbg(fmt.Sprintf("pane proc exited code=%d err=%v", code, err))
	m.mu.Lock()
	m.removePaneLocked(p)
	m.mu.Unlock()
}

func (m *Manager) removePaneLocked(p *Pane) {
	w := p.win
	if w == nil {
		return
	}
	newRoot, ok := removeFromTree(w.root, p)
	if !ok {
		return
	}
	if w.zoomed {
		m.unzoomLocked(w)
	}
	p.pty.Close()
	p.proc.Close()
	w.root = newRoot
	if w.lastPane == p {
		w.lastPane = nil
	}
	if newRoot == nil {
		m.removeWindowLocked(w)
		return
	}
	if w.active == p {
		w.active = collectPanes(w.root)[0]
	}
	m.relayoutLocked(w)
	m.comp.invalidate()
	m.dirty = true
}

func (m *Manager) removeWindowLocked(w *Window) {
	idx := -1
	for i, x := range m.windows {
		if x == w {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}
	if m.lastWin == w {
		m.lastWin = nil
	}
	m.windows = append(m.windows[:idx], m.windows[idx+1:]...)
	if len(m.windows) == 0 {
		m.closeLocked()
		return
	}
	if m.renumber {
		for i, x := range m.windows {
			x.num = m.baseIndex + i
		}
		m.nextWin = m.baseIndex + len(m.windows)
	}
	if m.active >= len(m.windows) {
		m.active = len(m.windows) - 1
	}
	m.relayoutLocked(m.windows[m.active])
	m.comp.invalidate()
	m.dirty = true
}

// ---- actions (assume lock held) ----

// splitLocked splits the active pane. ratioPct, when >0, is the percentage of
// space given to the new pane (split-window -p).
func (m *Manager) splitLocked(horizontal bool, ratioPct int) {
	w := m.activeWindowLocked()
	if w == nil || w.active == nil {
		return
	}
	if w.zoomed {
		m.unzoomLocked(w)
	}
	dir := splitH // stacked (top/bottom)
	if horizontal {
		dir = splitV // side by side (left/right)
	}
	p, err := m.newPaneLocked(m.cols, m.bodyRows())
	if err != nil {
		m.showMessageLocked("split-window failed: " + err.Error())
		return
	}
	p.win = w
	if !w.root.splitLeaf(w.active, dir, p) {
		p.pty.Close()
		p.proc.Close()
		return
	}
	if ratioPct > 0 && ratioPct < 100 {
		if nd := findSplitParent(w.root, p); nd != nil {
			nd.ratio = 1 - float64(ratioPct)/100
		}
	}
	w.lastPane = w.active
	w.active = p
	m.relayoutLocked(w)
	m.comp.invalidate()
	m.dirty = true
}

// findSplitParent returns the split node whose b-child leaf holds p.
func findSplitParent(n *node, p *Pane) *node {
	if n == nil || n.dir == leaf {
		return nil
	}
	if n.b != nil && n.b.dir == leaf && n.b.pane == p {
		return n
	}
	if r := findSplitParent(n.a, p); r != nil {
		return r
	}
	return findSplitParent(n.b, p)
}

func (m *Manager) setActivePaneLocked(w *Window, p *Pane) {
	if w.active != p {
		w.lastPane = w.active
		w.active = p
		m.dirty = true
	}
}

func (m *Manager) selectPaneNextLocked() {
	w := m.activeWindowLocked()
	if w == nil {
		return
	}
	if w.zoomed {
		m.unzoomLocked(w)
	}
	ps := w.panes()
	for i, p := range ps {
		if p == w.active {
			m.setActivePaneLocked(w, ps[(i+1)%len(ps)])
			break
		}
	}
	m.dirty = true
}

func (m *Manager) lastPaneLocked() {
	w := m.activeWindowLocked()
	if w == nil || w.lastPane == nil {
		return
	}
	for _, p := range w.panes() {
		if p == w.lastPane {
			if w.zoomed {
				m.unzoomLocked(w)
			}
			m.setActivePaneLocked(w, p)
			return
		}
	}
	w.lastPane = nil
}

func (m *Manager) selectPaneDirLocked(dx, dy int) {
	w := m.activeWindowLocked()
	if w == nil || w.active == nil {
		return
	}
	if w.zoomed {
		m.unzoomLocked(w)
	}
	prs, _ := m.windowLayout(w)
	var cur rect
	for _, pr := range prs {
		if pr.pane == w.active {
			cur = pr.r
		}
	}
	ccx, ccy := cur.x+cur.w/2, cur.y+cur.h/2
	best := (*Pane)(nil)
	bestScore := 1 << 30
	for _, pr := range prs {
		if pr.pane == w.active {
			continue
		}
		cx, cy := pr.r.x+pr.r.w/2, pr.r.y+pr.r.h/2
		if dx != 0 { // horizontal move
			if (cx-ccx)*dx <= 0 {
				continue
			}
			score := abs(cx-ccx)*1 + abs(cy-ccy)*4
			if score < bestScore {
				bestScore, best = score, pr.pane
			}
		} else { // vertical move
			if (cy-ccy)*dy <= 0 {
				continue
			}
			score := abs(cy-ccy)*1 + abs(cx-ccx)*4
			if score < bestScore {
				bestScore, best = score, pr.pane
			}
		}
	}
	if best != nil {
		m.setActivePaneLocked(w, best)
	}
}

// zoomToggleLocked toggles full-window zoom of the active pane.
func (m *Manager) zoomToggleLocked() {
	w := m.activeWindowLocked()
	if w == nil || w.active == nil {
		return
	}
	if !w.zoomed && len(w.panes()) < 2 {
		return // nothing to zoom to
	}
	if w.zoomed {
		m.unzoomLocked(w)
	} else {
		w.zoomed = true
		m.relayoutLocked(w)
		m.comp.invalidate()
		m.dirty = true
	}
}

func (m *Manager) unzoomLocked(w *Window) {
	if !w.zoomed {
		return
	}
	w.zoomed = false
	m.relayoutLocked(w)
	m.comp.invalidate()
	m.dirty = true
}

// resizeLocked grows the active pane toward side ('L','R','U','D') by amount
// cells, moving whichever adjacent divider lies on that side; if the pane has no
// divider on that side (it touches the window edge) the opposite divider is
// moved outward instead, so the pane always grows.
func (m *Manager) resizeLocked(side byte, amount int) {
	w := m.activeWindowLocked()
	if w == nil || w.active == nil || w.zoomed {
		return
	}
	prs, divs := m.windowLayout(w)
	var cur rect
	found := false
	for _, pr := range prs {
		if pr.pane == w.active {
			cur, found = pr.r, true
		}
	}
	if !found {
		return
	}

	var left, right, top, bottom *divider
	for i := range divs {
		d := &divs[i]
		if d.vertical && spanOverlapsV(*d, cur) {
			if d.x == cur.x-1 {
				left = d
			} else if d.x == cur.x+cur.w {
				right = d
			}
		} else if !d.vertical && spanOverlapsH(*d, cur) {
			if d.y == cur.y-1 {
				top = d
			} else if d.y == cur.y+cur.h {
				bottom = d
			}
		}
	}

	moveV := func(d *divider, delta int) {
		setRatioCells(*d, (d.x-d.area.x)+delta, d.area.w)
	}
	moveH := func(d *divider, delta int) {
		setRatioCells(*d, (d.y-d.area.y)+delta, d.area.h)
	}

	changed := true
	switch side {
	case 'R':
		switch {
		case right != nil:
			moveV(right, amount)
		case left != nil:
			moveV(left, -amount)
		default:
			changed = false
		}
	case 'L':
		switch {
		case left != nil:
			moveV(left, -amount)
		case right != nil:
			moveV(right, amount)
		default:
			changed = false
		}
	case 'D':
		switch {
		case bottom != nil:
			moveH(bottom, amount)
		case top != nil:
			moveH(top, -amount)
		default:
			changed = false
		}
	case 'U':
		switch {
		case top != nil:
			moveH(top, -amount)
		case bottom != nil:
			moveH(bottom, amount)
		default:
			changed = false
		}
	}
	if changed {
		m.relayoutLocked(w)
		m.comp.invalidate()
		m.dirty = true
	}
}

// spanOverlapsV reports whether a vertical divider's row span overlaps r.
func spanOverlapsV(d divider, r rect) bool {
	return r.y < d.y+d.length && r.y+r.h > d.y
}

// spanOverlapsH reports whether a horizontal divider's column span overlaps r.
func spanOverlapsH(d divider, r rect) bool {
	return r.x < d.x+d.length && r.x+r.w > d.x
}

// setRatioCells sets a split's ratio so the boundary sits `boundary` cells into
// an area of `total` cells, keeping at least one cell on each side.
func setRatioCells(d divider, boundary, total int) {
	if total < 3 {
		return
	}
	if boundary < 1 {
		boundary = 1
	}
	if boundary > total-2 {
		boundary = total - 2
	}
	d.owner.ratio = float64(boundary) / float64(total-1)
}

func (m *Manager) renameWindowLocked(name string) {
	if w := m.activeWindowLocked(); w != nil && name != "" {
		w.name = name
		m.dirty = true
	}
}

func (m *Manager) killPaneLocked() {
	w := m.activeWindowLocked()
	if w != nil && w.active != nil {
		w.active.proc.Kill()
	}
}

func (m *Manager) nextWindowLocked() {
	if len(m.windows) > 0 {
		m.activateWindowLocked((m.active + 1) % len(m.windows))
	}
}

func (m *Manager) prevWindowLocked() {
	if n := len(m.windows); n > 0 {
		m.activateWindowLocked((m.active - 1 + n) % n)
	}
}

func (m *Manager) lastWindowLocked() {
	if m.lastWin == nil {
		return
	}
	for i, w := range m.windows {
		if w == m.lastWin {
			m.activateWindowLocked(i)
			return
		}
	}
	m.lastWin = nil
}

func (m *Manager) selectWindowNumLocked(num int) {
	for i, w := range m.windows {
		if w.num == num {
			m.activateWindowLocked(i)
			return
		}
	}
}

func (m *Manager) activateWindowLocked(idx int) {
	if idx < 0 || idx >= len(m.windows) {
		return
	}
	if cur := m.activeWindowLocked(); cur != nil && idx != m.active {
		m.lastWin = cur
	}
	m.active = idx
	m.relayoutLocked(m.windows[idx])
	m.comp.invalidate()
	m.dirty = true
}

func (m *Manager) sendPrefixLocked() {
	p := m.activePaneLocked()
	if p == nil {
		return
	}
	p.pty.In.Write(encodeKeyBytes(m.prefix))
}

func (m *Manager) pasteBufferLocked() {
	p := m.activePaneLocked()
	if p == nil {
		return
	}
	go func() {
		text := getClipboard()
		if text == "" {
			return
		}
		text = strings.ReplaceAll(text, "\r\n", "\r")
		text = strings.ReplaceAll(text, "\n", "\r")
		p.pty.In.Write([]byte(text))
	}()
}

// showMessageLocked displays a transient message on the status line.
func (m *Manager) showMessageLocked(text string) {
	m.msg = text
	m.msgUntil = time.Now().Add(m.displayTime)
	m.comp.invalidate()
	m.dirty = true
}

// windowLayout returns the pane rectangles and dividers for w. A zoomed window
// collapses to its active pane filling the whole body (no dividers).
func (m *Manager) windowLayout(w *Window) (prs []paneRect, divs []divider) {
	body := rect{0, m.bodyTop(), m.cols, m.bodyRows()}
	if w.zoomed && w.active != nil {
		return []paneRect{{pane: w.active, r: body}}, nil
	}
	w.root.walk(body, &prs, &divs)
	return prs, divs
}

// relayoutLocked recomputes pane rectangles for w and resizes panes whose size
// changed (which makes their shells redraw at the new size).
func (m *Manager) relayoutLocked(w *Window) {
	prs, _ := m.windowLayout(w)
	for _, pr := range prs {
		if pr.r.w != pr.pane.w || pr.r.h != pr.pane.h {
			pr.pane.w, pr.pane.h = pr.r.w, pr.r.h
			pr.pane.pty.Resize(int16(pr.r.w), int16(pr.r.h))
			pr.pane.screen.Resize(pr.r.w, pr.r.h)
		}
	}
}

// ---- input / dispatch (called by the input parser) ----

// MatchPrefix reports whether key is the configured prefix key.
func (m *Manager) MatchPrefix(key config.Key) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return key == m.prefix
}

// RunRootBind runs a no-prefix (bind -n) command if one is bound to key.
func (m *Manager) RunRootBind(key config.Key) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cmds, ok := m.rootBinds[key]; ok {
		m.dispatchAllLocked(cmds)
		return true
	}
	return false
}

// RunBind runs the prefix-table command bound to key, if any. It always
// "consumes" the key (returns) so the command key isn't sent to the shell.
func (m *Manager) RunBind(key config.Key) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cmds, ok := m.binds[key]; ok {
		m.dispatchAllLocked(cmds)
	}
}

// RunCommandLine parses a tmux command line (from command-prompt) and runs it.
func (m *Manager) RunCommandLine(line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, toks := range config.SplitCommands(config.Tokenize(line)) {
		for _, cmd := range config.ParseCommands(toks) {
			m.dispatchLocked(cmd)
		}
	}
}

func (m *Manager) dispatchAllLocked(cmds []config.Command) {
	for _, c := range cmds {
		m.dispatchLocked(c)
	}
}

func (m *Manager) dispatchLocked(cmd config.Command) {
	switch cmd.Name {
	case "new-window", "neww":
		m.newWindowLocked()
	case "split-window", "splitw":
		m.splitLocked(hasFlag(cmd.Args, "-h"), flagValueInt(cmd.Args, "-p"))
	case "select-pane", "selectp":
		switch {
		case hasFlag(cmd.Args, "-L"):
			m.selectPaneDirLocked(-1, 0)
		case hasFlag(cmd.Args, "-R"):
			m.selectPaneDirLocked(1, 0)
		case hasFlag(cmd.Args, "-U"):
			m.selectPaneDirLocked(0, -1)
		case hasFlag(cmd.Args, "-D"):
			m.selectPaneDirLocked(0, 1)
		default:
			m.selectPaneNextLocked()
		}
	case "last-pane", "lastp":
		m.lastPaneLocked()
	case "swap-pane", "swapp":
		if hasFlag(cmd.Args, "-U") {
			m.swapPaneLocked(-1)
		} else {
			m.swapPaneLocked(1)
		}
	case "rotate-window", "rotatew":
		m.rotateWindowLocked()
	case "break-pane", "breakp":
		m.breakPaneLocked()
	case "select-layout", "selectl":
		if name := strings.TrimSpace(nonFlagArgs(cmd.Args)); name != "" {
			if !m.applyLayoutLocked(m.activeWindowLocked(), name) {
				m.showMessageLocked("unknown layout: " + name)
			}
		} else {
			m.nextLayoutLocked(m.activeWindowLocked())
		}
	case "next-layout", "nextl":
		m.nextLayoutLocked(m.activeWindowLocked())
	case "resize-pane", "resizep":
		switch {
		case hasFlag(cmd.Args, "-Z"):
			m.zoomToggleLocked()
		case hasFlag(cmd.Args, "-L"):
			m.resizeLocked('L', resizeAmount(cmd.Args))
		case hasFlag(cmd.Args, "-R"):
			m.resizeLocked('R', resizeAmount(cmd.Args))
		case hasFlag(cmd.Args, "-U"):
			m.resizeLocked('U', resizeAmount(cmd.Args))
		case hasFlag(cmd.Args, "-D"):
			m.resizeLocked('D', resizeAmount(cmd.Args))
	}
	case "rename-window", "renamew":
		if name := strings.TrimSpace(nonFlagArgs(cmd.Args)); name != "" {
			m.renameWindowLocked(name)
		} else if w := m.activeWindowLocked(); w != nil {
			m.startPromptLocked("(rename-window) ", w.name, func(s string) {
				m.mu.Lock()
				m.renameWindowLocked(s)
				m.mu.Unlock()
			})
		}
	case "copy-mode":
		m.enterCopyLocked()
		if hasFlag(cmd.Args, "-u") {
			m.copyDoLocked(CopyPageUp)
		}
	case "paste-buffer", "pasteb":
		m.pasteBufferLocked()
	case "send-keys", "send":
		if p := m.activePaneLocked(); p != nil {
			if b := sendKeysBytes(cmd.Args); len(b) > 0 {
				pane := p
				go pane.pty.In.Write(b)
			}
		}
	case "kill-pane", "killp":
		m.killPaneLocked()
	case "kill-window", "killw":
		if w := m.activeWindowLocked(); w != nil {
			for _, p := range w.panes() {
				p.proc.Kill()
			}
		}
	case "next-window", "next":
		m.nextWindowLocked()
	case "previous-window", "prev":
		m.prevWindowLocked()
	case "last-window", "last":
		m.lastWindowLocked()
	case "select-window", "selectw":
		m.selectWindowTargetLocked(cmd.Args)
	case "choose-window", "choose-tree", "choose-session":
		m.openChooseWindowLocked()
	case "list-keys", "lsk":
		m.openListKeysLocked()
	case "command-prompt":
		m.startPromptLocked(":", "", func(s string) {
			if strings.TrimSpace(s) != "" {
				m.RunCommandLine(s)
			}
		})
	case "confirm-before", "confirm":
		m.confirmBeforeLocked(cmd.Args)
	case "display-message", "display":
		text := nonFlagArgs(cmd.Args)
		if text == "" {
			text = "[#S] #I:#W"
		}
		m.showMessageLocked(expandFormat(text, m.formatCtxLocked()))
	case "display-panes", "displayp":
		m.panesUntil = time.Now().Add(m.panesTime)
		m.comp.invalidate()
		m.dirty = true
	case "clock-mode", "clock":
		m.showMessageLocked(time.Now().Format("15:04:05 Mon Jan 2 2006"))
	case "set", "set-option", "setw", "set-window-option", "bind", "bind-key", "unbind", "unbind-key":
		c := config.New()
		c.ParseTokens(append([]string{cmd.Name}, cmd.Args...))
		m.applyConfigLocked(c)
		for _, warn := range c.Warnings {
			m.showMessageLocked(warn)
		}
	case "source-file", "source":
		c := config.New()
		for _, a := range cmd.Args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if err := c.Load(config.ExpandHome(a)); err != nil {
				m.showMessageLocked("source-file: " + err.Error())
				return
			}
		}
		m.applyConfigLocked(c)
	case "if-shell", "if":
		m.ifShellLocked(cmd.Args)
	case "detach-client", "detach":
		if m.detachHook != nil {
			go m.detachHook()
		} else {
			m.closeLocked()
		}
	case "kill-server", "kill-session":
		m.closeLocked()
	case "send-prefix":
		m.sendPrefixLocked()
	default:
		m.showMessageLocked("unknown command: " + cmd.Name)
	}
}

// confirmBeforeLocked implements confirm-before [-p prompt] command [args...].
func (m *Manager) confirmBeforeLocked(args []string) {
	promptText := "Confirm? (y/n)"
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] == "-p" && i+1 < len(args) {
			promptText = args[i+1]
			i += 2
			continue
		}
		i++
	}
	if i >= len(args) {
		return
	}
	wrapped := config.Command{Name: args[i], Args: args[i+1:]}
	m.startPromptLocked(promptText+" ", "", func(s string) {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "y") {
			m.mu.Lock()
			m.dispatchLocked(wrapped)
			m.mu.Unlock()
		}
	})
}

// ifShellLocked implements runtime if-shell: the condition runs in the
// background; the chosen branch is dispatched when it finishes.
func (m *Manager) ifShellLocked(args []string) {
	var rest []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) < 2 {
		return
	}
	cond, thenCmd := rest[0], rest[1]
	elseCmd := ""
	if len(rest) > 2 {
		elseCmd = rest[2]
	}
	go func() {
		ok := config.RunShellCondition(cond)
		if ok {
			m.RunCommandLine(thenCmd)
		} else if elseCmd != "" {
			m.RunCommandLine(elseCmd)
		}
	}()
}

// selectWindowTargetLocked handles select-window -t targets, including the
// relative forms :+ :- :^ :$ and plain numbers.
func (m *Manager) selectWindowTargetLocked(args []string) {
	t := ""
	for i, a := range args {
		if a == "-t" && i+1 < len(args) {
			t = args[i+1]
			break
		}
		if !strings.HasPrefix(a, "-") {
			t = a
			break
		}
	}
	t = strings.TrimPrefix(t, ":")
	switch {
	case t == "+" || strings.HasPrefix(t, "+"):
		m.nextWindowLocked()
	case t == "-" || strings.HasPrefix(t, "-"):
		m.prevWindowLocked()
	case t == "^":
		m.activateWindowLocked(0)
	case t == "$":
		m.activateWindowLocked(len(m.windows) - 1)
	case t == "!":
		m.lastWindowLocked()
	default:
		if n, ok := parseTarget(t); ok {
			m.selectWindowNumLocked(n)
		}
	}
}

// DisplayPanesActive reports whether the display-panes overlay is showing.
func (m *Manager) DisplayPanesActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Before(m.panesUntil)
}

// DisplayPanesKey handles a keypress during display-panes: a pane digit
// selects that pane; any key dismisses the overlay. Returns whether the key
// was a digit that selected a pane.
func (m *Manager) DisplayPanesKey(r rune) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.panesUntil = time.Time{}
	m.comp.invalidate()
	m.dirty = true
	if r < '0' || r > '9' {
		return false
	}
	n := int(r - '0')
	w := m.activeWindowLocked()
	if w == nil {
		return true
	}
	for _, p := range w.panes() {
		if p.num == n {
			m.setActivePaneLocked(w, p)
			return true
		}
	}
	return true
}

// SendInput forwards raw bytes to the active pane's shell (outside the lock so
// a blocked child can't stall the UI).
func (m *Manager) SendInput(b []byte) {
	m.mu.Lock()
	p := m.activePaneLocked()
	m.mu.Unlock()
	if p != nil {
		p.pty.In.Write(b)
	}
}

// Resize updates the host size and re-lays-out every window.
func (m *Manager) Resize(cols, rows int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cols, m.rows = cols, rows
	for _, w := range m.windows {
		m.relayoutLocked(w)
	}
	m.comp.invalidate()
	m.dirty = true
}

// Attach points the compositor at a client connection, sets the client's size,
// and forces a full repaint. Used by the server when a client connects.
func (m *Manager) Attach(out io.Writer, cols, rows int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		dbg("attach: manager closed")
		return
	}
	m.comp.out = out
	m.cols, m.rows = cols, rows
	for _, w := range m.windows {
		m.relayoutLocked(w)
	}
	m.comp.invalidate()
	m.dirty = true
	dbg("attach: out set, dirty")
}

// Detach stops sending output to any client (shells keep running). The next
// Attach forces a full repaint.
func (m *Manager) Detach() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.comp.out = io.Discard
	m.comp.invalidate()
}

// SetDetachHook installs the function detach-client should run (server mode).
func (m *Manager) SetDetachHook(f func()) {
	m.mu.Lock()
	m.detachHook = f
	m.mu.Unlock()
}

// Close detaches: stop managing and signal Done.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeLocked()
}

func (m *Manager) closeLocked() {
	if m.closed {
		return
	}
	dbg("closeLocked")
	m.closed = true
	for _, w := range m.windows {
		for _, p := range w.panes() {
			p.pty.Close()
			p.proc.Close()
		}
	}
	close(m.done)
}

func (m *Manager) activeWindowLocked() *Window {
	if m.active < 0 || m.active >= len(m.windows) {
		return nil
	}
	return m.windows[m.active]
}

func (m *Manager) activePaneLocked() *Pane {
	if w := m.activeWindowLocked(); w != nil {
		return w.active
	}
	return nil
}

// ---- render loop ----

// DebugLog, if set, receives diagnostic messages from the manager.
var DebugLog func(string)

func dbg(s string) {
	if DebugLog != nil {
		DebugLog(s)
	}
}

func (m *Manager) renderLoop() {
	t := time.NewTicker(25 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
		// expire transient overlays
		now := time.Now()
		if m.msg != "" && now.After(m.msgUntil) {
			m.msg = ""
			m.comp.invalidate()
			m.dirty = true
		}
		if !m.panesUntil.IsZero() && now.After(m.panesUntil) {
			m.panesUntil = time.Time{}
			m.comp.invalidate()
			m.dirty = true
		}
		if m.dirty {
			m.dirty = false
			func() {
				defer func() {
					if r := recover(); r != nil {
						dbg(fmt.Sprintf("render PANIC: %v", r))
					}
				}()
				m.renderLocked()
			}()
		}
		m.mu.Unlock()
	}
}

func (m *Manager) renderLocked() {
	f := newFrame(m.cols, m.rows)
	w := m.activeWindowLocked()
	if w != nil {
		prs, divs := m.windowLayout(w)

		var arect rect
		haveA := false
		for _, pr := range prs {
			if pr.pane == w.active && pr.pane.copy != nil {
				m.blitCopy(f, pr.pane, pr.r)
			} else {
				f.blit(pr.pane.screen, pr.r.x, pr.r.y, pr.r.w, pr.r.h)
			}
			if pr.pane == w.active {
				arect, haveA = pr.r, true
			}
		}
		m.drawDividers(f, divs, arect, haveA)
		if haveA {
			if cp := w.active.copy; cp != nil {
				f.curX, f.curY, f.curVis = arect.x+cp.cx, arect.y+cp.cy, true
			} else {
				cx, cy, vis := w.active.screen.Cursor()
				f.curX, f.curY, f.curVis = arect.x+cx, arect.y+cy, vis
			}
		}
		if time.Now().Before(m.panesUntil) {
			m.drawPaneNumbers(f, prs)
		}
	}
	if m.overlay != nil {
		m.drawOverlay(f)
		f.curVis = false
	}
	switch {
	case m.prompt != nil:
		m.drawPrompt(f)
	case m.msg != "":
		m.drawMessage(f)
	case m.statusOn:
		m.drawStatus(f)
	}
	m.comp.render(f)
}

func (m *Manager) drawDividers(f *Frame, divs []divider, arect rect, haveA bool) {
	border := m.borderActiveAtt
	dim := m.borderAtt
	for _, d := range divs {
		for i := 0; i < d.length; i++ {
			var x, y int
			var r rune
			if d.vertical {
				x, y, r = d.x, d.y+i, '│'
			} else {
				x, y, r = d.x+i, d.y, '─'
			}
			att := dim
			if haveA && onActiveBorder(x, y, arect) {
				att = border
			}
			// junction handling
			if cur := f.cells[clamp(y, 0, f.h-1)][clamp(x, 0, f.w-1)].R; (cur == '│' || cur == '─' || cur == '┼') && cur != r {
				r = '┼'
			}
			f.set(x, y, vt.Cell{R: r, A: att})
		}
	}
}

func onActiveBorder(x, y int, a rect) bool {
	if (x == a.x-1 || x == a.x+a.w) && y >= a.y && y < a.y+a.h {
		return true
	}
	if (y == a.y-1 || y == a.y+a.h) && x >= a.x && x < a.x+a.w {
		return true
	}
	return false
}

// drawPaneNumbers overlays each pane's index (display-panes).
func (m *Manager) drawPaneNumbers(f *Frame, prs []paneRect) {
	att := vt.Attr{FG: "30", BG: "44", Bold: true}
	actAtt := vt.Attr{FG: "30", BG: "43", Bold: true}
	w := m.activeWindowLocked()
	for _, pr := range prs {
		label := " " + strconv.Itoa(pr.pane.num) + " "
		a := att
		if w != nil && pr.pane == w.active {
			a = actAtt
		}
		x := pr.r.x + pr.r.w/2 - len(label)/2
		y := pr.r.y + pr.r.h/2
		putString(f, x, y, label, a)
	}
}

// formatCtxLocked builds the format context for status-line expansion.
func (m *Manager) formatCtxLocked() formatCtx {
	ctx := formatCtx{
		session: m.session,
		cols:    m.cols,
		rows:    m.rows,
		windows: len(m.windows),
	}
	if w := m.activeWindowLocked(); w != nil {
		ctx.window = w
		ctx.current = true
		ctx.pane = w.active
	}
	return ctx
}

func (m *Manager) drawStatus(f *Frame) {
	row := m.statusRow()
	for x := 0; x < m.cols; x++ {
		f.set(x, row, vt.Cell{R: ' ', A: m.statusAtt})
	}

	base := m.formatCtxLocked()

	left := parseSpans(expandFormat(m.statusLeft, base), m.statusAtt)
	left = truncateSpans(left, m.statusLeftLen)
	right := parseSpans(expandFormat(m.statusRight, base), m.statusAtt)
	right = truncateSpans(right, m.statusRightLen)

	// window list
	var winSpans []span
	activeW := m.activeWindowLocked()
	for i, w := range m.windows {
		if i > 0 {
			winSpans = append(winSpans, span{text: m.winSep, attr: m.statusAtt})
		}
		ctx := base
		ctx.window = w
		ctx.current = w == activeW
		ctx.last = w == m.lastWin
		ctx.pane = w.active
		fmtStr, att := m.winFmt, m.winAtt
		if ctx.current {
			fmtStr, att = m.winCurFmt, m.winCurAtt
		}
		winSpans = append(winSpans, parseSpans(expandFormat(fmtStr, ctx), att)...)
	}

	leftEnd := putSpansClip(f, 0, row, left, m.cols)
	rightW := spanWidth(right)
	rightStart := m.cols - rightW
	if rightStart < leftEnd {
		rightStart = leftEnd
	}
	putSpansClip(f, rightStart, row, right, m.cols)

	// windows between leftEnd and rightStart, per status-justify
	avail := rightStart - leftEnd
	winW := spanWidth(winSpans)
	x := leftEnd
	switch m.statusJustify {
	case "centre", "center":
		if winW < avail {
			x = leftEnd + (avail-winW)/2
		}
	case "right":
		if winW < avail {
			x = rightStart - winW - 1
		}
	}
	putSpansClip(f, x, row, winSpans, rightStart)
}

// drawMessage renders a transient display-message over the status row.
func (m *Manager) drawMessage(f *Frame) {
	row := m.statusRow()
	for x := 0; x < m.cols; x++ {
		f.set(x, row, vt.Cell{R: ' ', A: m.msgAtt})
	}
	putString(f, 0, row, m.msg, m.msgAtt)
}

// putSpansClip writes styled spans at (x,y) clipped to maxX, returning the
// final x.
func putSpansClip(f *Frame, x, y int, spans []span, maxX int) int {
	if maxX > f.w {
		maxX = f.w
	}
	for _, sp := range spans {
		for _, r := range sp.text {
			if x >= maxX {
				return x
			}
			if x >= 0 {
				f.set(x, y, vt.Cell{R: r, A: sp.attr})
			}
			x++
		}
	}
	return x
}

// truncateSpans limits spans to n runes total (n<=0 means no limit).
func truncateSpans(spans []span, n int) []span {
	if n <= 0 {
		return spans
	}
	var out []span
	for _, sp := range spans {
		r := []rune(sp.text)
		if len(r) > n {
			out = append(out, span{text: string(r[:n]), attr: sp.attr})
			return out
		}
		out = append(out, sp)
		n -= len(r)
		if n == 0 {
			return out
		}
	}
	return out
}

func putString(f *Frame, x, y int, s string, a vt.Attr) {
	for _, r := range s {
		if x >= f.w {
			break
		}
		if x >= 0 {
			f.set(x, y, vt.Cell{R: r, A: a})
		}
		x++
	}
}

// ---- helpers ----

// resizeAmount returns the first numeric argument, or 2 by default.
func resizeAmount(args []string) int {
	for _, a := range args {
		if n, err := strconv.Atoi(a); err == nil && n > 0 {
			return n
		}
	}
	return 2
}

// nonFlagArgs joins the arguments that aren't flags (don't start with '-').
func nonFlagArgs(args []string) string {
	var rest []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			rest = append(rest, a)
		}
	}
	return strings.Join(rest, " ")
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// flagValueInt returns the integer following flag, or 0.
func flagValueInt(args []string, flag string) int {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			if n, err := strconv.Atoi(args[i+1]); err == nil {
				return n
			}
		}
	}
	return 0
}

func parseTarget(s string) (int, bool) {
	s = strings.TrimPrefix(s, ":")
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
