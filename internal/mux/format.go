package mux

import (
	"os"
	"strconv"
	"strings"
	"time"

	"panewright/internal/vt"
)

// formatCtx carries the values available to tmux #{} format expansion. Fields
// that don't apply in a given context (e.g. window fields in status-left) are
// zero and expand to "".
type formatCtx struct {
	session string
	window  *Window
	current bool // window is the active window
	last    bool // window is the previously active window
	pane    *Pane
	cols    int
	rows    int
	windows int
}

// expandFormat expands #{...} variables, #X shorthands and strftime %-codes.
// #[...] style directives are passed through untouched for parseSpans.
func expandFormat(s string, ctx formatCtx) string {
	var b strings.Builder
	t := time.Now()
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '#' && i+1 < len(s):
			next := s[i+1]
			switch next {
			case '{':
				body, end, ok := matchBrace(s, i+1)
				if !ok {
					b.WriteByte(ch)
					continue
				}
				b.WriteString(expandVar(body, ctx))
				i = end
			case '[':
				// style directive: copy verbatim through the closing ]
				j := strings.IndexByte(s[i:], ']')
				if j < 0 {
					b.WriteString(s[i:])
					return b.String()
				}
				b.WriteString(s[i : i+j+1])
				i += j
			case '#':
				b.WriteByte('#')
				i++
			default:
				if v, ok := shorthand(next, ctx); ok {
					b.WriteString(v)
					i++
				} else {
					b.WriteByte(ch)
				}
			}
		case ch == '%' && i+1 < len(s):
			out, consumed := strftimeCode(s[i+1], t)
			if consumed {
				b.WriteString(out)
				i++
			} else {
				b.WriteByte(ch)
			}
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// matchBrace returns the contents of a balanced {...} starting at s[open]=='{'
// and the index of the closing brace.
func matchBrace(s string, open int) (body string, end int, ok bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[open+1 : i], i, true
			}
		}
	}
	return "", 0, false
}

// expandVar evaluates the body of a #{...} construct.
func expandVar(body string, ctx formatCtx) string {
	// conditional: #{?cond,then,else}
	if strings.HasPrefix(body, "?") {
		cond, thenS, elseS := splitConditional(body[1:])
		v := expandVar(cond, ctx)
		if v != "" && v != "0" {
			return expandFormat(thenS, ctx)
		}
		return expandFormat(elseS, ctx)
	}
	// length limit: #{=N:variable}
	if strings.HasPrefix(body, "=") {
		if i := strings.IndexByte(body, ':'); i > 0 {
			n, err := strconv.Atoi(body[1:i])
			v := expandVar(body[i+1:], ctx)
			if err == nil {
				r := []rune(v)
				if n >= 0 && len(r) > n {
					return string(r[:n])
				}
				if n < 0 && len(r) > -n {
					return string(r[len(r)+n:])
				}
			}
			return v
		}
	}
	return variable(body, ctx)
}

// splitConditional splits "cond,then,else" at top-level commas (commas inside
// nested #{...} don't count).
func splitConditional(s string) (cond, thenS, elseS string) {
	parts := make([]string, 0, 3)
	depth, start := 0, 0
	for i := 0; i < len(s) && len(parts) < 2; i++ {
		switch {
		case s[i] == '{':
			depth++
		case s[i] == '}':
			depth--
		case s[i] == ',' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}

func shorthand(ch byte, ctx formatCtx) (string, bool) {
	switch ch {
	case 'S':
		return variable("session_name", ctx), true
	case 'I':
		return variable("window_index", ctx), true
	case 'W':
		return variable("window_name", ctx), true
	case 'F':
		return variable("window_flags", ctx), true
	case 'P':
		return variable("pane_index", ctx), true
	case 'T':
		return variable("pane_title", ctx), true
	case 'H':
		return variable("host", ctx), true
	case 'h':
		return variable("host_short", ctx), true
	}
	return "", false
}

func variable(name string, ctx formatCtx) string {
	switch name {
	case "session_name", "client_session":
		return ctx.session
	case "session_windows":
		return strconv.Itoa(ctx.windows)
	case "host", "hostname":
		h, _ := os.Hostname()
		return h
	case "host_short", "hostname_short":
		h, _ := os.Hostname()
		if i := strings.IndexByte(h, '.'); i > 0 {
			h = h[:i]
		}
		return h
	case "window_index":
		if ctx.window != nil {
			return strconv.Itoa(ctx.window.num)
		}
	case "window_name":
		if ctx.window != nil {
			return ctx.window.name
		}
	case "window_flags":
		if ctx.window == nil {
			return ""
		}
		var f string
		if ctx.window.zoomed {
			f += "Z"
		}
		if ctx.current {
			f += "*"
		} else if ctx.last {
			f += "-"
		}
		return f
	case "window_active":
		return boolVar(ctx.current)
	case "window_zoomed_flag":
		if ctx.window != nil {
			return boolVar(ctx.window.zoomed)
		}
	case "window_panes":
		if ctx.window != nil {
			return strconv.Itoa(len(ctx.window.panes()))
		}
	case "pane_index":
		if ctx.pane != nil {
			return strconv.Itoa(ctx.pane.num)
		}
	case "pane_title", "pane_current_command":
		if ctx.pane != nil {
			return ctx.pane.title()
		}
	case "client_width", "window_width":
		return strconv.Itoa(ctx.cols)
	case "client_height", "window_height":
		return strconv.Itoa(ctx.rows)
	case "version":
		return "panewright"
	}
	return ""
}

func boolVar(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// strftimeCode expands one %-code with time t. Unknown codes are left alone.
func strftimeCode(code byte, t time.Time) (string, bool) {
	switch code {
	case 'a':
		return t.Format("Mon"), true
	case 'A':
		return t.Format("Monday"), true
	case 'b', 'h':
		return t.Format("Jan"), true
	case 'B':
		return t.Format("January"), true
	case 'd':
		return t.Format("02"), true
	case 'e':
		return t.Format("_2"), true
	case 'H':
		return t.Format("15"), true
	case 'I':
		return t.Format("03"), true
	case 'j':
		return strconv.Itoa(t.YearDay()), true
	case 'm':
		return t.Format("01"), true
	case 'M':
		return t.Format("04"), true
	case 'p':
		return t.Format("PM"), true
	case 'S':
		return t.Format("05"), true
	case 'y':
		return t.Format("06"), true
	case 'Y':
		return t.Format("2006"), true
	case 'Z':
		return t.Format("MST"), true
	case 'F':
		return t.Format("2006-01-02"), true
	case 'T':
		return t.Format("15:04:05"), true
	case 'R':
		return t.Format("15:04"), true
	case '%':
		return "%", true
	}
	return "", false
}

// span is a run of status-line text sharing one attribute.
type span struct {
	text string
	attr vt.Attr
}

// parseSpans splits an expanded format string on #[...] style directives into
// styled spans. Styles accumulate; #[default] (or fg=default etc.) resets
// toward base.
func parseSpans(s string, base vt.Attr) []span {
	var spans []span
	cur := base
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			spans = append(spans, span{text: text.String(), attr: cur})
			text.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && i+1 < len(s) && s[i+1] == '[' {
			j := strings.IndexByte(s[i:], ']')
			if j < 0 {
				break
			}
			flush()
			applyStyleWithBase(&cur, s[i+2:i+j], base)
			i += j
			continue
		}
		text.WriteByte(s[i])
	}
	flush()
	return spans
}

// spanWidth is the total rune count of spans.
func spanWidth(spans []span) int {
	n := 0
	for _, sp := range spans {
		n += len([]rune(sp.text))
	}
	return n
}

// putSpans writes styled spans at (x,y), clipped to the frame, returning the
// final x.
func putSpans(f *Frame, x, y int, spans []span) int {
	for _, sp := range spans {
		for _, r := range sp.text {
			if x >= f.w {
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
