// Package hostterm manages the real console panewright is running in: putting it
// into raw / VT pass-through mode and reporting its size.
package hostterm

import (
	"fmt"

	"golang.org/x/sys/windows"
)

var (
	kernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleCP    = kernel32.NewProc("GetConsoleCP")
	procSetConsoleCP    = kernel32.NewProc("SetConsoleCP")
	procGetConsoleOutCP = kernel32.NewProc("GetConsoleOutputCP")
	procSetConsoleOutCP = kernel32.NewProc("SetConsoleOutputCP")
)

const cpUTF8 = 65001

// Console mode flags (defined locally to avoid depending on which constants a
// given x/sys/windows version exports).
const (
	enableProcessedInput            = 0x0001
	enableLineInput                 = 0x0002
	enableEchoInput                 = 0x0004
	enableWindowInput               = 0x0008
	enableMouseInput                = 0x0010
	enableQuickEditMode             = 0x0040
	enableExtendedFlags             = 0x0080
	enableProcessedOutput           = 0x0001
	enableVirtualTerminalProcessing = 0x0004
	disableNewlineAutoReturn        = 0x0008
)

// State holds the original console modes and code pages so they can be
// restored on exit.
type State struct {
	stdin     windows.Handle
	stdout    windows.Handle
	origIn    uint32
	origOut   uint32
	origInCP  uint32
	origOutCP uint32
}

// MakeRaw switches stdin to raw VT input and stdout to VT processing, so the
// host console becomes a transparent pipe for the child's terminal stream.
func MakeRaw() (*State, error) {
	stdin, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return nil, fmt.Errorf("get stdin: %w", err)
	}
	stdout, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return nil, fmt.Errorf("get stdout: %w", err)
	}

	var inMode, outMode uint32
	if err := windows.GetConsoleMode(stdin, &inMode); err != nil {
		return nil, fmt.Errorf("get stdin mode: %w", err)
	}
	if err := windows.GetConsoleMode(stdout, &outMode); err != nil {
		return nil, fmt.Errorf("get stdout mode: %w", err)
	}

	// Raw input for ReadConsoleInput: no echo/line/processed/quick-edit; enable
	// mouse, window-resize, and extended flags. We intentionally do NOT enable
	// virtual-terminal-input — we read native INPUT_RECORDs and encode keys
	// ourselves, which makes mouse handling reliable.
	newIn := inMode &^ uint32(enableEchoInput|enableLineInput|enableProcessedInput|enableQuickEditMode)
	newIn |= enableMouseInput | enableWindowInput | enableExtendedFlags
	if err := windows.SetConsoleMode(stdin, newIn); err != nil {
		return nil, fmt.Errorf("set stdin mode: %w", err)
	}

	newOut := outMode | enableProcessedOutput | enableVirtualTerminalProcessing | disableNewlineAutoReturn
	if err := windows.SetConsoleMode(stdout, newOut); err != nil {
		windows.SetConsoleMode(stdin, inMode) // roll back
		return nil, fmt.Errorf("set stdout mode: %w", err)
	}

	// Switch the console to UTF-8 so multibyte output (box-drawing, Unicode in
	// TUIs) is interpreted correctly rather than as a legacy code page.
	inCP, _, _ := procGetConsoleCP.Call()
	outCP, _, _ := procGetConsoleOutCP.Call()
	procSetConsoleCP.Call(cpUTF8)
	procSetConsoleOutCP.Call(cpUTF8)

	return &State{
		stdin:     stdin,
		stdout:    stdout,
		origIn:    inMode,
		origOut:   outMode,
		origInCP:  uint32(inCP),
		origOutCP: uint32(outCP),
	}, nil
}

// Restore returns the console to the modes captured by MakeRaw.
func (s *State) Restore() {
	if s == nil {
		return
	}
	windows.SetConsoleMode(s.stdin, s.origIn)
	windows.SetConsoleMode(s.stdout, s.origOut)
	if s.origInCP != 0 {
		procSetConsoleCP.Call(uintptr(s.origInCP))
	}
	if s.origOutCP != 0 {
		procSetConsoleOutCP.Call(uintptr(s.origOutCP))
	}
}

// Size returns the current visible console size in character cells.
func Size() (cols, rows int16, err error) {
	stdout, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return 0, 0, fmt.Errorf("get stdout: %w", err)
	}
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(stdout, &info); err != nil {
		return 0, 0, fmt.Errorf("get screen buffer info: %w", err)
	}
	cols = info.Window.Right - info.Window.Left + 1
	rows = info.Window.Bottom - info.Window.Top + 1
	return cols, rows, nil
}
