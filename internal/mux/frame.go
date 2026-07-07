package mux

import (
	"fmt"
	"io"
	"strings"

	"panewright/internal/vt"
)

// Frame is a full host-screen snapshot the compositor builds each redraw.
type Frame struct {
	w, h   int
	cells  [][]vt.Cell
	curX   int
	curY   int
	curVis bool
}

func newFrame(w, h int) *Frame {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	f := &Frame{w: w, h: h, curVis: true}
	f.cells = make([][]vt.Cell, h)
	for y := range f.cells {
		f.cells[y] = make([]vt.Cell, w)
		for x := range f.cells[y] {
			f.cells[y][x] = vt.Cell{R: ' '}
		}
	}
	return f
}

func (f *Frame) set(x, y int, c vt.Cell) {
	if x < 0 || y < 0 || x >= f.w || y >= f.h {
		return
	}
	if c.R == 0 {
		c.R = ' '
	}
	f.cells[y][x] = c
}

// blit copies a pane's screen grid into the frame at the given origin, clipped
// to the destination of size w x h.
func (f *Frame) blit(s *vt.Screen, ox, oy, w, h int) {
	sw, sh := s.Size()
	for y := 0; y < h && y < sh; y++ {
		for x := 0; x < w && x < sw; x++ {
			f.set(ox+x, oy+y, s.CellAt(x, y))
		}
	}
}

// compositor diffs successive frames and emits the minimal VT to update the
// host, eliminating flicker when many panes redraw.
type compositor struct {
	out  io.Writer
	prev *Frame
}

func newCompositor(out io.Writer) *compositor { return &compositor{out: out} }

// invalidate forces the next render to be a full repaint.
func (c *compositor) invalidate() { c.prev = nil }

func (c *compositor) render(cur *Frame) {
	var b strings.Builder
	if c.prev == nil || c.prev.w != cur.w || c.prev.h != cur.h {
		b.WriteString("\x1b[0m\x1b[2J")
		c.prev = nil
	}
	for y := 0; y < cur.h; y++ {
		x := 0
		for x < cur.w {
			if c.prev != nil && c.prev.cells[y][x] == cur.cells[y][x] {
				x++
				continue
			}
			b.WriteString(moveTo(y+1, x+1))
			var lastAttr vt.Attr
			started := false
			for x < cur.w {
				cc := cur.cells[y][x]
				if c.prev != nil && c.prev.cells[y][x] == cc {
					break
				}
				if !started || cc.A != lastAttr {
					b.WriteString(cc.A.SGR())
					lastAttr = cc.A
					started = true
				}
				r := cc.R
				if r == 0 {
					r = ' '
				}
				b.WriteRune(r)
				x++
			}
			b.WriteString("\x1b[0m")
		}
	}
	b.WriteString(moveTo(cur.curY+1, cur.curX+1))
	if cur.curVis {
		b.WriteString("\x1b[?25h")
	} else {
		b.WriteString("\x1b[?25l")
	}
	dbg(fmt.Sprintf("comp.render write %d bytes", b.Len()))
	io.WriteString(c.out, b.String())
	dbg("comp.render write done")
	c.prev = cur
}

func moveTo(row, col int) string {
	return fmt.Sprintf("\x1b[%d;%dH", row, col)
}
