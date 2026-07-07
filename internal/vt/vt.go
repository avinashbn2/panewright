// Package vt is a small terminal emulator. Each hosted shell's VT-encoded
// output is fed into a Screen, which maintains a character grid (text +
// SGR attributes + cursor). panewright uses this to repaint a window when it
// becomes active and, later, to composite split panes into one frame.
//
// It implements the common subset used by interactive shells: printable
// text, CR/LF/BS/TAB, the cursor-movement and erase CSI sequences, and SGR
// colour/attribute tracking. Unknown sequences are consumed and ignored so
// the parser never desynchronises.
package vt

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Attr is the rendered style of a cell, stored as SGR fragments so it can be
// re-emitted verbatim on repaint.
type Attr struct {
	FG        string // e.g. "31", "38;5;200", "38;2;10;20;30"; "" = default
	BG        string
	Bold      bool
	Underline bool
	Reverse   bool
}

// SGR returns the escape sequence that selects this attribute (reset first).
func (a Attr) SGR() string {
	if a == (Attr{}) {
		return "\x1b[0m"
	}
	parts := make([]string, 0, 6)
	if a.Bold {
		parts = append(parts, "1")
	}
	if a.Underline {
		parts = append(parts, "4")
	}
	if a.Reverse {
		parts = append(parts, "7")
	}
	if a.FG != "" {
		parts = append(parts, a.FG)
	}
	if a.BG != "" {
		parts = append(parts, a.BG)
	}
	return "\x1b[0;" + strings.Join(parts, ";") + "m"
}

// Cell is one character cell on the grid.
type Cell struct {
	R rune
	A Attr
}

type parserState int

const (
	stGround parserState = iota
	stEsc
	stCSI
)

// Screen is a fixed-size terminal grid driven by Write. Lines that scroll off
// the top are retained in a bounded scrollback buffer for copy mode.
type Screen struct {
	w, h    int
	cells   [][]Cell
	cx, cy  int
	cur     Attr
	visible bool

	scrollback [][]Cell // lines that have scrolled off the top (oldest first)
	maxScroll  int      // cap on scrollback length

	state   parserState
	params  []byte // raw CSI parameter/intermediate bytes
	skipOSC bool   // swallowing an OSC string
	oscBuf  []byte // accumulated OSC payload (title etc.)
	title   string // last OSC 0/2 title set by the application

	utf8buf  []byte // partial UTF-8 sequence being assembled
	utf8need int    // total bytes expected for the current sequence
}

// New returns a blank Screen of the given size (minimum 1x1).
func New(w, h int) *Screen {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	s := &Screen{w: w, h: h, visible: true, maxScroll: 2000}
	s.alloc()
	return s
}

func (s *Screen) alloc() {
	s.cells = make([][]Cell, s.h)
	for y := range s.cells {
		s.cells[y] = make([]Cell, s.w)
		for x := range s.cells[y] {
			s.cells[y][x] = Cell{R: ' '}
		}
	}
}

// Size reports the grid dimensions.
func (s *Screen) Size() (w, h int) { return s.w, s.h }

// Cursor reports the current cursor cell and whether it is visible.
func (s *Screen) Cursor() (x, y int, visible bool) { return s.cx, s.cy, s.visible }

// CellAt returns the cell at x,y (clamped). Used by the compositor and tests.
func (s *Screen) CellAt(x, y int) Cell {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x >= s.w {
		x = s.w - 1
	}
	if y >= s.h {
		y = s.h - 1
	}
	return s.cells[y][x]
}

// Resize changes the grid size, preserving overlapping content.
func (s *Screen) Resize(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if w == s.w && h == s.h {
		return
	}
	old := s.cells
	oldH := s.h
	s.w, s.h = w, h
	s.alloc()
	for y := 0; y < oldH && y < h; y++ {
		for x := 0; x < len(old[y]) && x < w; x++ {
			s.cells[y][x] = old[y][x]
		}
	}
	s.clamp()
}

func (s *Screen) clamp() {
	if s.cx < 0 {
		s.cx = 0
	}
	if s.cy < 0 {
		s.cy = 0
	}
	if s.cx >= s.w {
		s.cx = s.w - 1
	}
	if s.cy >= s.h {
		s.cy = s.h - 1
	}
}

// Write feeds VT-encoded bytes into the screen model.
func (s *Screen) Write(p []byte) (int, error) {
	for i := 0; i < len(p); i++ {
		s.step(p[i])
	}
	return len(p), nil
}

func (s *Screen) step(b byte) {
	switch s.state {
	case stGround:
		s.ground(b)
	case stEsc:
		switch b {
		case '[':
			s.state = stCSI
			s.params = s.params[:0]
		case ']': // OSC: accumulate to ST/BEL (title etc.)
			s.state = stGround
			s.skipOSC = true
			s.oscBuf = s.oscBuf[:0]
		default:
			s.state = stGround
		}
	case stCSI:
		// parameters: digits, ';', '?', etc. final byte is 0x40-0x7e
		if b >= 0x40 && b <= 0x7e {
			s.csi(b)
			s.state = stGround
		} else {
			s.params = append(s.params, b)
		}
	}
}

func (s *Screen) ground(b byte) {
	if s.skipOSC {
		// accumulate OSC string until BEL or ESC (start of ST)
		if b == 0x07 || b == 0x1b {
			s.skipOSC = false
			s.finishOSC()
			if b == 0x1b {
				s.state = stEsc // consume the ST's trailing backslash
			}
			return
		}
		if len(s.oscBuf) < 4096 {
			s.oscBuf = append(s.oscBuf, b)
		}
		return
	}
	// Continue assembling a multibyte UTF-8 character.
	if s.utf8need > 0 {
		if b >= 0x80 && b < 0xc0 { // continuation byte
			s.utf8buf = append(s.utf8buf, b)
			if len(s.utf8buf) >= s.utf8need {
				r, _ := utf8.DecodeRune(s.utf8buf)
				s.put(r)
				s.utf8need = 0
			}
			return
		}
		// invalid/truncated sequence: emit replacement, then handle b below
		s.put('�')
		s.utf8need = 0
	}
	switch b {
	case 0x1b:
		s.state = stEsc
	case '\r':
		s.cx = 0
	case '\n':
		s.lineFeed()
	case '\b':
		if s.cx > 0 {
			s.cx--
		}
	case '\t':
		s.cx = ((s.cx / 8) + 1) * 8
		if s.cx >= s.w {
			s.cx = s.w - 1
		}
	case 0x07:
		// bell: ignore
	default:
		switch {
		case b < 0x20:
			// other C0 control: ignore
		case b < 0x80:
			s.put(rune(b))
		default:
			// UTF-8 lead byte: start assembling
			n := utf8LeadLen(b)
			if n < 2 {
				s.put('�')
				return
			}
			s.utf8need = n
			s.utf8buf = append(s.utf8buf[:0], b)
		}
	}
}

// finishOSC interprets a completed OSC payload: codes 0 and 2 set the title.
func (s *Screen) finishOSC() {
	p := string(s.oscBuf)
	if strings.HasPrefix(p, "0;") || strings.HasPrefix(p, "2;") {
		s.title = p[2:]
	}
}

// Title returns the window title last set by the application via OSC 0/2.
func (s *Screen) Title() string { return s.title }

// SetMaxScroll caps the scrollback history length (0 disables scrollback).
func (s *Screen) SetMaxScroll(n int) {
	if n < 0 {
		n = 0
	}
	s.maxScroll = n
	if len(s.scrollback) > n {
		s.scrollback = s.scrollback[len(s.scrollback)-n:]
	}
}

// utf8LeadLen returns the total byte length implied by a UTF-8 lead byte, or 0
// if b isn't a valid lead.
func utf8LeadLen(b byte) int {
	switch {
	case b&0xe0 == 0xc0:
		return 2
	case b&0xf0 == 0xe0:
		return 3
	case b&0xf8 == 0xf0:
		return 4
	}
	return 0
}

func (s *Screen) put(r rune) {
	if s.cx >= s.w {
		s.cx = 0
		s.lineFeed()
	}
	s.cells[s.cy][s.cx] = Cell{R: r, A: s.cur}
	s.cx++
}

func (s *Screen) lineFeed() {
	s.cy++
	if s.cy >= s.h {
		s.cy = s.h - 1
		s.scrollUp()
	}
}

func (s *Screen) scrollUp() {
	top := s.cells[0]
	if s.maxScroll > 0 {
		line := make([]Cell, len(top))
		copy(line, top)
		s.scrollback = append(s.scrollback, line)
		if len(s.scrollback) > s.maxScroll {
			s.scrollback = s.scrollback[len(s.scrollback)-s.maxScroll:]
		}
	}
	copy(s.cells, s.cells[1:])
	for x := range top {
		top[x] = Cell{R: ' '}
	}
	s.cells[s.h-1] = top
}

// TotalRows is the number of rows addressable by RowAt: scrollback plus the
// live screen.
func (s *Screen) TotalRows() int { return len(s.scrollback) + s.h }

// Scrollback reports how many lines of history are retained.
func (s *Screen) Scrollback() int { return len(s.scrollback) }

// RowAt returns the row at absolute index absY (0 = oldest scrollback line,
// rising through history into the live screen). The returned slice is owned by
// the screen and must not be mutated.
func (s *Screen) RowAt(absY int) []Cell {
	if absY < 0 {
		absY = 0
	}
	if absY < len(s.scrollback) {
		return s.scrollback[absY]
	}
	y := absY - len(s.scrollback)
	if y >= s.h {
		y = s.h - 1
	}
	return s.cells[y]
}

func (s *Screen) csi(final byte) {
	raw := string(s.params)
	private := strings.HasPrefix(raw, "?")
	raw = strings.TrimPrefix(raw, "?")
	args := parseParams(raw)

	switch final {
	case 'A': // cursor up
		s.cy -= max1(arg(args, 0, 1))
	case 'B': // cursor down
		s.cy += max1(arg(args, 0, 1))
	case 'C': // cursor forward
		s.cx += max1(arg(args, 0, 1))
	case 'D': // cursor back
		s.cx -= max1(arg(args, 0, 1))
	case 'E': // next line
		s.cy += max1(arg(args, 0, 1))
		s.cx = 0
	case 'F': // prev line
		s.cy -= max1(arg(args, 0, 1))
		s.cx = 0
	case 'G': // column
		s.cx = arg(args, 0, 1) - 1
	case 'd': // row
		s.cy = arg(args, 0, 1) - 1
	case 'H', 'f': // position
		s.cy = arg(args, 0, 1) - 1
		s.cx = arg(args, 1, 1) - 1
	case 'J': // erase display
		s.eraseDisplay(arg(args, 0, 0))
	case 'K': // erase line
		s.eraseLine(arg(args, 0, 0))
	case 'm': // SGR
		s.sgr(args)
	case 'h':
		if private && hasArg(args, 25) {
			s.visible = true
		}
	case 'l':
		if private && hasArg(args, 25) {
			s.visible = false
		}
	}
	s.clamp()
}

func (s *Screen) eraseDisplay(mode int) {
	switch mode {
	case 0: // cursor to end
		s.eraseLineRange(s.cy, s.cx, s.w)
		for y := s.cy + 1; y < s.h; y++ {
			s.eraseLineRange(y, 0, s.w)
		}
	case 1: // start to cursor
		for y := 0; y < s.cy; y++ {
			s.eraseLineRange(y, 0, s.w)
		}
		s.eraseLineRange(s.cy, 0, s.cx+1)
	case 2, 3: // whole screen
		for y := 0; y < s.h; y++ {
			s.eraseLineRange(y, 0, s.w)
		}
	}
}

func (s *Screen) eraseLine(mode int) {
	switch mode {
	case 0:
		s.eraseLineRange(s.cy, s.cx, s.w)
	case 1:
		s.eraseLineRange(s.cy, 0, s.cx+1)
	case 2:
		s.eraseLineRange(s.cy, 0, s.w)
	}
}

func (s *Screen) eraseLineRange(y, x0, x1 int) {
	if y < 0 || y >= s.h {
		return
	}
	for x := x0; x < x1 && x < s.w; x++ {
		if x < 0 {
			continue
		}
		s.cells[y][x] = Cell{R: ' '}
	}
}

func (s *Screen) sgr(args []int) {
	if len(args) == 0 {
		s.cur = Attr{}
		return
	}
	for i := 0; i < len(args); i++ {
		n := args[i]
		switch {
		case n == 0:
			s.cur = Attr{}
		case n == 1:
			s.cur.Bold = true
		case n == 22:
			s.cur.Bold = false
		case n == 4:
			s.cur.Underline = true
		case n == 24:
			s.cur.Underline = false
		case n == 7:
			s.cur.Reverse = true
		case n == 27:
			s.cur.Reverse = false
		case (n >= 30 && n <= 37) || (n >= 90 && n <= 97):
			s.cur.FG = strconv.Itoa(n)
		case n == 39:
			s.cur.FG = ""
		case (n >= 40 && n <= 47) || (n >= 100 && n <= 107):
			s.cur.BG = strconv.Itoa(n)
		case n == 49:
			s.cur.BG = ""
		case n == 38 || n == 48:
			frag, consumed := extendedColor(args[i:])
			if n == 38 {
				s.cur.FG = frag
			} else {
				s.cur.BG = frag
			}
			i += consumed
		}
	}
}

// extendedColor parses "38;5;n" / "38;2;r;g;b" starting at args[0]==38/48 and
// returns the SGR fragment plus how many extra params it consumed.
func extendedColor(args []int) (frag string, consumed int) {
	if len(args) < 2 {
		return "", 0
	}
	switch args[1] {
	case 5:
		if len(args) < 3 {
			return "", 1
		}
		return strconv.Itoa(args[0]) + ";5;" + strconv.Itoa(args[2]), 2
	case 2:
		if len(args) < 5 {
			return "", 1
		}
		return strconv.Itoa(args[0]) + ";2;" +
			strconv.Itoa(args[2]) + ";" +
			strconv.Itoa(args[3]) + ";" +
			strconv.Itoa(args[4]), 4
	}
	return "", 1
}

// --- helpers ---

func parseParams(raw string) []int {
	if raw == "" {
		return nil
	}
	fields := strings.Split(raw, ";")
	out := make([]int, len(fields))
	for i, f := range fields {
		out[i], _ = strconv.Atoi(f)
	}
	return out
}

func arg(args []int, i, def int) int {
	if i < len(args) && args[i] != 0 {
		return args[i]
	}
	return def
}

func hasArg(args []int, v int) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
