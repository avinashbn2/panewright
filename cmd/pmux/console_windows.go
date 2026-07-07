package main

import "golang.org/x/sys/windows"

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	user32           = windows.NewLazySystemDLL("user32.dll")
	procAllocConsole = kernel32.NewProc("AllocConsole")
	procFreeConsole  = kernel32.NewProc("FreeConsole")
	procGetConWindow = kernel32.NewProc("GetConsoleWindow")
	procShowWindow   = user32.NewProc("ShowWindow")
)

const swHide = 0

// allocHiddenConsole gives the (detached) server process its own console and
// hides its window. ConPTY needs the host process to have a console/window
// station to spawn child shells, but the server must not show one.
func allocHiddenConsole() {
	procFreeConsole.Call()
	procAllocConsole.Call()
	if hwnd, _, _ := procGetConWindow.Call(); hwnd != 0 {
		procShowWindow.Call(hwnd, swHide)
	}
}

// bindConsoleStdHandles points the server's standard handles at its own
// console. Go's exec starts the server with STARTF_USESTDHANDLES (std = NUL),
// and CreateProcess propagates non-console std handles verbatim into ConPTY
// children instead of giving them pseudo-console handles
// (microsoft/terminal#4061) — the pane shells then hit EOF on stdin and exit
// immediately. Rebinding to CONIN$/CONOUT$ restores the normal "parent std
// handles are console handles" case, so children get fresh handles to their
// own pseudo console.
func bindConsoleStdHandles() error {
	open := func(name string) (windows.Handle, error) {
		return windows.CreateFile(windows.StringToUTF16Ptr(name),
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil, windows.OPEN_EXISTING, 0, 0)
	}
	in, err := open("CONIN$")
	if err != nil {
		// No console at all (e.g. started detached): make a hidden one.
		allocHiddenConsole()
		if in, err = open("CONIN$"); err != nil {
			return err
		}
	}
	out, err := open("CONOUT$")
	if err != nil {
		return err
	}
	if err := windows.SetStdHandle(windows.STD_INPUT_HANDLE, in); err != nil {
		return err
	}
	if err := windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, out); err != nil {
		return err
	}
	return windows.SetStdHandle(windows.STD_ERROR_HANDLE, out)
}
