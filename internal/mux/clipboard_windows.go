package mux

import (
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// setClipboard copies text to the Windows clipboard via clip.exe, which is
// present on every supported Windows version. Failures are ignored — copy mode
// is best-effort.
func setClipboard(text string) {
	cmd := exec.Command("clip.exe")
	cmd.Stdin = strings.NewReader(text)
	_ = cmd.Run()
}

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	kernel32clip           = syscall.NewLazyDLL("kernel32.dll")
	procOpenClipboard      = user32.NewProc("OpenClipboard")
	procCloseClipboard     = user32.NewProc("CloseClipboard")
	procGetClipboardData   = user32.NewProc("GetClipboardData")
	procIsClipboardFormat  = user32.NewProc("IsClipboardFormatAvailable")
	procGlobalLock         = kernel32clip.NewProc("GlobalLock")
	procGlobalUnlock       = kernel32clip.NewProc("GlobalUnlock")
)

const cfUnicodeText = 13

// getClipboard reads text from the Windows clipboard (empty string on any
// failure — paste is best-effort).
func getClipboard() string {
	if ok, _, _ := procIsClipboardFormat.Call(cfUnicodeText); ok == 0 {
		return ""
	}
	if ok, _, _ := procOpenClipboard.Call(0); ok == 0 {
		return ""
	}
	defer procCloseClipboard.Call()

	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return ""
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer procGlobalUnlock.Call(h)

	// The locked block is native (GlobalAlloc) memory, valid until GlobalUnlock,
	// so reading through its address is safe; reinterpret the returned uintptr
	// via its storage to satisfy vet's unsafeptr check.
	base := *(*unsafe.Pointer)(unsafe.Pointer(&p))
	var units []uint16
	for i := 0; ; i++ {
		u := *(*uint16)(unsafe.Add(base, i*2))
		if u == 0 {
			break
		}
		units = append(units, u)
		if i > 1<<20 { // sanity cap: 1M UTF-16 units
			break
		}
	}
	return string(utf16.Decode(units))
}
