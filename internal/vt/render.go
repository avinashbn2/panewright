package vt

import "strings"

// Paint returns VT bytes that draw the whole screen onto a host terminal: it
// homes the cursor, writes every row with attributes, then positions the real
// cursor where the model's cursor is. Used when a window becomes active.
func (s *Screen) Paint() string {
	var b strings.Builder
	b.Grow(s.w*s.h + 64)
	b.WriteString("\x1b[H") // home
	s.writeRows(&b, 1, 1, s.w)
	// place the hardware cursor (1-based)
	b.WriteString("\x1b[")
	b.WriteString(itoa(s.cy + 1))
	b.WriteByte(';')
	b.WriteString(itoa(s.cx + 1))
	b.WriteByte('H')
	if s.visible {
		b.WriteString("\x1b[?25h")
	} else {
		b.WriteString("\x1b[?25l")
	}
	return b.String()
}

// RenderInto draws the grid into b positioned at the given 1-based host
// origin, clipped to width cols. Used by the compositor to place a pane.
func (s *Screen) RenderInto(b *strings.Builder, originRow, originCol, cols int) {
	s.writeRows(b, originRow, originCol, cols)
}

// writeRows emits each grid row at (originRow+y, originCol), coalescing runs of
// equal attributes into a single SGR change. cols clips the row width.
func (s *Screen) writeRows(b *strings.Builder, originRow, originCol, cols int) {
	width := s.w
	if cols < width {
		width = cols
	}
	var last Attr
	haveLast := false
	for y := 0; y < s.h; y++ {
		// position at start of this row
		b.WriteString("\x1b[")
		b.WriteString(itoa(originRow + y))
		b.WriteByte(';')
		b.WriteString(itoa(originCol))
		b.WriteByte('H')
		for x := 0; x < width; x++ {
			c := s.cells[y][x]
			if !haveLast || c.A != last {
				b.WriteString(c.A.SGR())
				last = c.A
				haveLast = true
			}
			r := c.R
			if r == 0 {
				r = ' '
			}
			b.WriteRune(r)
		}
	}
	b.WriteString("\x1b[0m")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
