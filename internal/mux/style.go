package mux

import (
	"strconv"
	"strings"

	"panewright/internal/vt"
)

// buildStatusAttr derives the status-bar attribute from tmux-style options:
// status-style (e.g. "bg=green,fg=black,bold") plus the standalone
// status-bg / status-fg options. Returns false if none were set.
func buildStatusAttr(opts map[string]string) (vt.Attr, bool) {
	var a vt.Attr
	set := false
	if s, ok := opts["status-style"]; ok {
		applyStyle(&a, s)
		set = true
	}
	if v, ok := opts["status-bg"]; ok {
		a.BG = colorSGR(v, true)
		set = true
	}
	if v, ok := opts["status-fg"]; ok {
		a.FG = colorSGR(v, false)
		set = true
	}
	return a, set
}

// applyStyle parses a comma-separated tmux style string into a.
func applyStyle(a *vt.Attr, style string) {
	applyStyleWithBase(a, style, vt.Attr{})
}

// applyStyleWithBase parses a tmux style string into a, resolving "default"
// (and the bare word "default"/"none") against base — the enclosing style, so
// #[default] inside a status format returns to the status-bar style.
func applyStyleWithBase(a *vt.Attr, style string, base vt.Attr) {
	for _, part := range strings.Split(style, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case part == "default", part == "none":
			*a = base
		case part == "bold", part == "bright":
			a.Bold = true
		case part == "nobold":
			a.Bold = false
		case part == "underscore", part == "underline":
			a.Underline = true
		case part == "nounderscore", part == "nounderline":
			a.Underline = false
		case part == "reverse":
			a.Reverse = true
		case part == "noreverse":
			a.Reverse = false
		case strings.HasPrefix(part, "bg="):
			if v := strings.TrimPrefix(part, "bg="); v == "default" {
				a.BG = base.BG
			} else {
				a.BG = colorSGR(v, true)
			}
		case strings.HasPrefix(part, "fg="):
			if v := strings.TrimPrefix(part, "fg="); v == "default" {
				a.FG = base.FG
			} else {
				a.FG = colorSGR(v, false)
			}
		}
	}
}

var baseColors = map[string]int{
	"black": 0, "red": 1, "green": 2, "yellow": 3,
	"blue": 4, "magenta": 5, "cyan": 6, "white": 7,
}

// colorSGR turns a tmux color name into the SGR fragment for a foreground or
// background. Returns "" for default/unknown (meaning: leave default).
func colorSGR(name string, bg bool) string {
	name = strings.TrimSpace(strings.ToLower(name))
	base := 30
	if bg {
		base = 40
	}
	if name == "" || name == "default" {
		return ""
	}
	if n, ok := baseColors[name]; ok {
		return strconv.Itoa(base + n)
	}
	if strings.HasPrefix(name, "bright") {
		if n, ok := baseColors[strings.TrimPrefix(name, "bright")]; ok {
			return strconv.Itoa(base + 60 + n) // 90..97 / 100..107
		}
	}
	if strings.HasPrefix(name, "#") && len(name) == 7 {
		if r, g, b, ok := parseHex(name[1:]); ok {
			if bg {
				return "48;2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b)
			}
			return "38;2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b)
		}
	}
	if strings.HasPrefix(name, "colour") || strings.HasPrefix(name, "color") {
		num := strings.TrimPrefix(strings.TrimPrefix(name, "colour"), "color")
		if n, err := strconv.Atoi(num); err == nil && n >= 0 && n <= 255 {
			if bg {
				return "48;5;" + strconv.Itoa(n)
			}
			return "38;5;" + strconv.Itoa(n)
		}
	}
	return ""
}

// parseHex decodes "rrggbb" into components.
func parseHex(s string) (r, g, b int, ok bool) {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff), true
}
