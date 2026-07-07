// Package coninput reads native Windows console input records (keyboard and
// mouse) via ReadConsoleInput. This is more reliable than VT input translation
// for mouse, and gives panewright full control over how keys are encoded for the
// child shell.
package coninput

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procReadConsoleInput = kernel32.NewProc("ReadConsoleInputW")
)

// Control-key-state bits (dwControlKeyState).
const (
	RightAltPressed  = 0x0001
	LeftAltPressed   = 0x0002
	RightCtrlPressed = 0x0004
	LeftCtrlPressed  = 0x0008
	ShiftPressed     = 0x0010
)

// Mouse event flags (dwEventFlags) and button-state bits.
const (
	MouseMoved      = 0x0001
	MouseDoubleClick = 0x0002
	MouseWheeled    = 0x0004
	MouseHWheeled   = 0x0008

	LeftButton = 0x0001
)

// EventType discriminates the Event union.
type EventType int

const (
	EventKey EventType = iota
	EventMouse
	EventResize
	EventOther
)

// Event is a decoded console input record.
type Event struct {
	Type EventType

	// Keyboard
	KeyDown bool
	VK      uint16
	Char    rune
	Ctrl    bool
	Alt     bool
	Shift   bool

	// Mouse (viewport-relative cell coordinates)
	X, Y        int
	ButtonState uint32
	MouseFlags  uint32
}

// raw INPUT_RECORD: a 2-byte event type, 2 bytes padding, then a 16-byte union.
type inputRecord struct {
	eventType uint16
	_         [2]byte
	data      [16]byte
}

type keyEventRecord struct {
	bKeyDown          int32
	wRepeatCount      uint16
	wVirtualKeyCode   uint16
	wVirtualScanCode  uint16
	unicodeChar       uint16
	dwControlKeyState uint32
}

type mouseEventRecord struct {
	mousePositionX  int16
	mousePositionY  int16
	buttonState     uint32
	controlKeyState uint32
	eventFlags      uint32
}

// Reader reads from the console input buffer.
type Reader struct {
	in  windows.Handle
	out windows.Handle
}

// Open opens the console input (CONIN$) and output (CONOUT$) buffers.
func Open() (*Reader, error) {
	in, err := openConsole("CONIN$")
	if err != nil {
		return nil, err
	}
	out, err := openConsole("CONOUT$")
	if err != nil {
		windows.CloseHandle(in)
		return nil, err
	}
	return &Reader{in: in, out: out}, nil
}

func openConsole(name string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
}

// Read blocks for the next batch of input records and returns decoded events.
func (r *Reader) Read() ([]Event, error) {
	var recs [32]inputRecord
	var n uint32
	ret, _, err := procReadConsoleInput.Call(
		uintptr(r.in),
		uintptr(unsafe.Pointer(&recs[0])),
		uintptr(len(recs)),
		uintptr(unsafe.Pointer(&n)),
	)
	if ret == 0 {
		return nil, err
	}

	winLeft, winTop := r.viewportOrigin()

	evs := make([]Event, 0, n)
	for i := 0; i < int(n); i++ {
		rec := &recs[i]
		switch rec.eventType {
		case 0x0001: // KEY_EVENT
			k := (*keyEventRecord)(unsafe.Pointer(&rec.data[0]))
			cs := k.dwControlKeyState
			evs = append(evs, Event{
				Type:    EventKey,
				KeyDown: k.bKeyDown != 0,
				VK:      k.wVirtualKeyCode,
				Char:    rune(k.unicodeChar),
				Ctrl:    cs&(LeftCtrlPressed|RightCtrlPressed) != 0,
				Alt:     cs&(LeftAltPressed|RightAltPressed) != 0,
				Shift:   cs&ShiftPressed != 0,
			})
		case 0x0002: // MOUSE_EVENT
			mo := (*mouseEventRecord)(unsafe.Pointer(&rec.data[0]))
			evs = append(evs, Event{
				Type:        EventMouse,
				X:           int(mo.mousePositionX) - winLeft,
				Y:           int(mo.mousePositionY) - winTop,
				ButtonState: mo.buttonState,
				MouseFlags:  mo.eventFlags,
			})
		case 0x0004: // WINDOW_BUFFER_SIZE_EVENT
			evs = append(evs, Event{Type: EventResize})
		default:
			evs = append(evs, Event{Type: EventOther})
		}
	}
	return evs, nil
}

// viewportOrigin returns the visible window's top-left in buffer coordinates so
// mouse positions can be made viewport-relative.
func (r *Reader) viewportOrigin() (left, top int) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(r.out, &info); err != nil {
		return 0, 0
	}
	return int(info.Window.Left), int(info.Window.Top)
}

// Close releases the console handles.
func (r *Reader) Close() {
	windows.CloseHandle(r.in)
	windows.CloseHandle(r.out)
}
