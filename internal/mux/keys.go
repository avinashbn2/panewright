package mux

import (
	"strings"

	"panewright/internal/config"
)

// specialKeySeq maps special key runes to the plain terminal sequences sent to
// a child shell.
var specialKeySeq = map[rune]string{
	config.KeyUp:     "\x1b[A",
	config.KeyDown:   "\x1b[B",
	config.KeyRight:  "\x1b[C",
	config.KeyLeft:   "\x1b[D",
	config.KeyHome:   "\x1b[H",
	config.KeyEnd:    "\x1b[F",
	config.KeyInsert: "\x1b[2~",
	config.KeyDelete: "\x1b[3~",
	config.KeyPgUp:   "\x1b[5~",
	config.KeyPgDn:   "\x1b[6~",
	config.KeyF1:     "\x1bOP",
	config.KeyF1 + 1: "\x1bOQ",
	config.KeyF1 + 2: "\x1bOR",
	config.KeyF1 + 3: "\x1bOS",
	config.KeyF1 + 4: "\x1b[15~",
	config.KeyF1 + 5: "\x1b[17~",
	config.KeyF1 + 6: "\x1b[18~",
	config.KeyF1 + 7: "\x1b[19~",
	config.KeyF1 + 8: "\x1b[20~",
	config.KeyF1 + 9: "\x1b[21~",
	config.KeyF1 + 10: "\x1b[23~",
	config.KeyF1 + 11: "\x1b[24~",
}

// encodeKeyBytes renders a parsed key as the bytes a terminal would send for
// it, honoring Ctrl/Alt modifiers. Used by send-keys and send-prefix.
func encodeKeyBytes(k config.Key) []byte {
	if seq, ok := specialKeySeq[k.Rune]; ok {
		return []byte(seq) // modifiers on special keys: send the plain form
	}
	var b []byte
	r := k.Rune
	if k.Ctrl {
		switch {
		case r >= 'a' && r <= 'z':
			b = []byte{byte(r-'a') + 1}
		case r >= 'A' && r <= 'Z':
			b = []byte{byte(r-'A') + 1}
		case r == ' ':
			b = []byte{0}
		default:
			b = []byte(string(r))
		}
	} else {
		b = []byte(string(r))
	}
	if k.Alt {
		return append([]byte{0x1b}, b...)
	}
	return b
}

// sendKeysBytes converts send-keys arguments into the byte stream for the
// pane: each argument is looked up as a key name; anything that doesn't parse
// as a single key is sent literally. With -l everything is literal.
func sendKeysBytes(args []string) []byte {
	literal := false
	var out []byte
	for _, a := range args {
		if !literal && strings.HasPrefix(a, "-") && len(a) > 1 && len(out) == 0 {
			if strings.Contains(a, "l") {
				literal = true
			}
			continue // flags (-l, -t target unsupported, -R, ...)
		}
		if !literal {
			if k, ok := config.ParseKey(a); ok && (len([]rune(a)) == 1 || isKeyName(a)) {
				out = append(out, encodeKeyBytes(k)...)
				continue
			}
		}
		out = append(out, []byte(a)...)
	}
	return out
}

// isKeyName reports whether s is a multi-character key spec (C-x, M-x, or a
// named key like Enter/F5) rather than literal text.
func isKeyName(s string) bool {
	if strings.HasPrefix(s, "C-") || strings.HasPrefix(s, "M-") || strings.HasPrefix(s, "S-") || strings.HasPrefix(s, "^") {
		return true
	}
	_, named := map[string]bool{
		"Space": true, "Enter": true, "Tab": true, "BSpace": true, "Escape": true,
		"Up": true, "Down": true, "Left": true, "Right": true,
		"Home": true, "End": true, "PPage": true, "PageUp": true, "PgUp": true,
		"NPage": true, "PageDown": true, "PgDn": true, "IC": true, "Insert": true,
		"DC": true, "Delete": true,
	}[s]
	if named {
		return true
	}
	if len(s) >= 2 && s[0] == 'F' {
		for _, c := range s[1:] {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
}
