package main

import (
	"strconv"

	"panewright/internal/config"
	"panewright/internal/coninput"
	"panewright/internal/mux"
)

// handleKey applies the prefix state machine to a key-down event and returns
// the new armed state.
func handleKey(mgr *mux.Manager, ev coninput.Event, armed bool) bool {
	// Modal layers capture all input ahead of the prefix machine.
	if mgr.PromptActive() {
		handlePromptKey(mgr, ev)
		return false
	}
	if mgr.OverlayActive() {
		handleOverlayKey(mgr, ev)
		return false
	}
	if mgr.CopyActive() {
		handleCopyKey(mgr, ev)
		return false
	}
	if mgr.DisplayPanesActive() {
		if mgr.DisplayPanesKey(rune(ev.Char)) {
			return false
		}
		// non-digit dismissed the overlay; fall through to normal handling
	}
	key, isKey := keyOf(ev)
	if armed {
		if isKey {
			mgr.RunBind(key)
		}
		return false
	}
	if isKey {
		if mgr.RunRootBind(key) {
			return false
		}
		if mgr.MatchPrefix(key) {
			return true
		}
	}
	if b := encodeKey(ev); len(b) > 0 {
		mgr.SendInput(b)
	}
	return false
}

// handlePromptKey feeds a key into the active status-line prompt.
func handlePromptKey(mgr *mux.Manager, ev coninput.Event) {
	switch ev.VK {
	case 0x0D: // Enter
		mgr.PromptCommit()
	case 0x1B: // Escape
		mgr.PromptCancel()
	case 0x08: // Backspace
		mgr.PromptBackspace()
	default:
		if ev.Char >= 0x20 {
			mgr.PromptRune(rune(ev.Char))
		}
	}
}

// handleOverlayKey drives a modal list (choose-window, list-keys).
func handleOverlayKey(mgr *mux.Manager, ev coninput.Event) {
	switch ev.VK {
	case 0x26: // Up
		mgr.OverlayMove(-1)
		return
	case 0x28: // Down
		mgr.OverlayMove(1)
		return
	case 0x21: // PageUp
		mgr.OverlayMove(-10)
		return
	case 0x22: // PageDown
		mgr.OverlayMove(10)
		return
	case 0x0D: // Enter
		mgr.OverlayCommit()
		return
	case 0x1B: // Escape
		mgr.OverlayCancel()
		return
	}
	switch ev.Char {
	case 'j':
		mgr.OverlayMove(1)
	case 'k':
		mgr.OverlayMove(-1)
	case 'q':
		mgr.OverlayCancel()
	default:
		if ev.Char >= '0' && ev.Char <= '9' {
			if n, err := strconv.Atoi(string(rune(ev.Char))); err == nil {
				mgr.OverlaySelect(n)
			}
		}
	}
}

// handleCopyKey maps a key to a copy-mode action.
func handleCopyKey(mgr *mux.Manager, ev coninput.Event) {
	if a, ok := copyVKActions[ev.VK]; ok {
		mgr.CopyDo(a)
		return
	}
	if ev.Ctrl && (ev.Char == 2 || ev.Char == 'b') { // Ctrl-b
		mgr.CopyDo(mux.CopyPageUp)
		return
	}
	if ev.Ctrl && (ev.Char == 6 || ev.Char == 'f') { // Ctrl-f
		mgr.CopyDo(mux.CopyPageDown)
		return
	}
	if a, ok := copyRuneActions[rune(ev.Char)]; ok {
		mgr.CopyDo(a)
	}
}

var copyVKActions = map[uint16]mux.CopyAction{
	0x26: mux.CopyUp, 0x28: mux.CopyDown, 0x25: mux.CopyLeft, 0x27: mux.CopyRight,
	0x21: mux.CopyPageUp, 0x22: mux.CopyPageDown,
	0x24: mux.CopyLineStart, 0x23: mux.CopyLineEnd,
	0x1B: mux.CopyCancel, 0x0D: mux.CopyCopy,
}

var copyRuneActions = map[rune]mux.CopyAction{
	'k': mux.CopyUp, 'j': mux.CopyDown, 'h': mux.CopyLeft, 'l': mux.CopyRight,
	'g': mux.CopyTop, 'G': mux.CopyBottom, '0': mux.CopyLineStart, '$': mux.CopyLineEnd,
	' ': mux.CopyStartSel, 'v': mux.CopyStartSel, 'y': mux.CopyCopy, 'q': mux.CopyCancel,
}

func handleMouse(mgr *mux.Manager, ev coninput.Event) {
	if !mgr.MouseEnabled() {
		return
	}
	if ev.MouseFlags&coninput.MouseWheeled != 0 {
		up := int32(ev.ButtonState) > 0 // high word sign: positive = wheel up
		mgr.MouseWheel(up, ev.X, ev.Y)
		return
	}
	if ev.MouseFlags&coninput.MouseHWheeled != 0 {
		return
	}
	left := ev.ButtonState&coninput.LeftButton != 0
	if ev.MouseFlags&coninput.MouseMoved != 0 {
		if left {
			mgr.MouseDrag(ev.X, ev.Y)
		}
		return
	}
	if left {
		mgr.MouseDown(ev.X, ev.Y)
	} else {
		mgr.MouseUp()
	}
}

// keyOf maps a key event to a config.Key for binding lookup, or reports false
// if it carries no actionable key (e.g. a bare modifier).
func keyOf(ev coninput.Event) (config.Key, bool) {
	if r, ok := specialRunes[ev.VK]; ok {
		return config.Key{Rune: r, Ctrl: ev.Ctrl, Alt: ev.Alt, Shift: ev.Shift}, true
	}
	if ev.Char == 0 {
		return config.Key{}, false
	}
	r := rune(ev.Char)
	if ev.Ctrl && ev.Char >= 1 && ev.Char <= 26 {
		r = rune('a' + ev.Char - 1) // control char -> letter
	}
	return config.Key{Rune: r, Ctrl: ev.Ctrl, Alt: ev.Alt}, true
}

// specialRunes maps virtual key codes for keys without a character to the
// config special runes used by bindings.
var specialRunes = map[uint16]rune{
	0x26: config.KeyUp,
	0x28: config.KeyDown,
	0x25: config.KeyLeft,
	0x27: config.KeyRight,
	0x24: config.KeyHome,
	0x23: config.KeyEnd,
	0x21: config.KeyPgUp,
	0x22: config.KeyPgDn,
	0x2D: config.KeyInsert,
	0x2E: config.KeyDelete,
	0x70: config.KeyF1, 0x71: config.KeyF1 + 1, 0x72: config.KeyF1 + 2, 0x73: config.KeyF1 + 3,
	0x74: config.KeyF1 + 4, 0x75: config.KeyF1 + 5, 0x76: config.KeyF1 + 6, 0x77: config.KeyF1 + 7,
	0x78: config.KeyF1 + 8, 0x79: config.KeyF1 + 9, 0x7A: config.KeyF1 + 10, 0x7B: config.KeyF1 + 11,
}

// vkCSI describes how to encode a special key: either a CSI letter form
// (ESC [ 1 ; mod letter) or a tilde form (ESC [ code ; mod ~).
type vkCSI struct {
	letter byte // non-zero: CSI-letter key (arrows, Home, End, F1-F4 via SS3)
	code   int  // non-zero: tilde-form key code
	ss3    bool // plain form uses SS3 (ESC O letter), e.g. F1-F4
}

var vkEnc = map[uint16]vkCSI{
	0x26: {letter: 'A'}, 0x28: {letter: 'B'}, 0x27: {letter: 'C'}, 0x25: {letter: 'D'},
	0x24: {letter: 'H'}, 0x23: {letter: 'F'},
	0x2D: {code: 2}, 0x2E: {code: 3}, 0x21: {code: 5}, 0x22: {code: 6},
	0x70: {letter: 'P', ss3: true}, 0x71: {letter: 'Q', ss3: true},
	0x72: {letter: 'R', ss3: true}, 0x73: {letter: 'S', ss3: true},
	0x74: {code: 15}, 0x75: {code: 17}, 0x76: {code: 18}, 0x77: {code: 19},
	0x78: {code: 20}, 0x79: {code: 21}, 0x7A: {code: 23}, 0x7B: {code: 24},
}

// encodeKey turns a key event into the bytes to send to the child shell,
// using xterm modifier encoding (ESC[1;5C etc.) when Ctrl/Alt/Shift are held.
func encodeKey(ev coninput.Event) []byte {
	if ev.VK == 0x08 { // Backspace -> DEL, the terminal convention
		if ev.Ctrl {
			return []byte{0x08} // Ctrl-Backspace -> BS
		}
		return []byte{0x7f}
	}
	if ev.Char == 0 {
		e, ok := vkEnc[ev.VK]
		if !ok {
			return nil
		}
		mod := 1
		if ev.Shift {
			mod++
		}
		if ev.Alt {
			mod += 2
		}
		if ev.Ctrl {
			mod += 4
		}
		switch {
		case e.letter != 0 && mod == 1 && e.ss3:
			return []byte{0x1b, 'O', e.letter}
		case e.letter != 0 && mod == 1:
			return []byte{0x1b, '[', e.letter}
		case e.letter != 0:
			return []byte("\x1b[1;" + strconv.Itoa(mod) + string(e.letter))
		case mod == 1:
			return []byte("\x1b[" + strconv.Itoa(e.code) + "~")
		default:
			return []byte("\x1b[" + strconv.Itoa(e.code) + ";" + strconv.Itoa(mod) + "~")
		}
	}
	buf := []byte(string(rune(ev.Char)))
	if ev.Alt {
		return append([]byte{0x1b}, buf...) // Alt -> ESC prefix
	}
	return buf
}
