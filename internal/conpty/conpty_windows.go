// Package conpty wraps the Windows Pseudo Console (ConPTY) API so panewright can
// host a child process (a shell) inside a virtual terminal and exchange
// VT-encoded I/O with it over pipes.
//
// See: https://learn.microsoft.com/en-us/windows/console/creating-a-pseudoconsole-session
package conpty

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procCreatePseudoConsole = kernel32.NewProc("CreatePseudoConsole")
	procResizePseudoConsole = kernel32.NewProc("ResizePseudoConsole")
	procClosePseudoConsole  = kernel32.NewProc("ClosePseudoConsole")
)

// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE: ties a spawned process to an HPCON.
const procThreadAttributePseudoConsole = 0x00020016

// handleAsPointer reinterprets a handle's numeric value as an unsafe.Pointer
// for APIs (like UpdateProcThreadAttribute with PSEUDOCONSOLE) whose "value"
// argument is the handle itself rather than a pointer to memory.
func handleAsPointer(h windows.Handle) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&h))
}

type coord struct {
	X, Y int16
}

// pack encodes a COORD into the single uintptr the ConPTY ABI expects
// (low word = X/columns, high word = Y/rows).
func (c coord) pack() uintptr {
	return uintptr(uint32(uint16(c.X)) | uint32(uint16(c.Y))<<16)
}

// ConPTY is a live pseudo console plus the pipe ends we use to drive it.
type ConPTY struct {
	hpc     windows.Handle
	inWrite windows.Handle // bytes we write here become the child's stdin
	outRead windows.Handle // the child's stdout/stderr arrives here

	// In is a writer to the child's input; Out is a reader of the child's
	// VT-encoded output. Both are backed by the pipe handles above.
	In  *os.File
	Out *os.File
}

// New creates a pseudo console of the given size (in character cells).
func New(cols, rows int16) (*ConPTY, error) {
	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("create input pipe: %w", err)
	}
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		return nil, fmt.Errorf("create output pipe: %w", err)
	}

	var hpc windows.Handle
	size := coord{X: cols, Y: rows}
	hr, _, _ := procCreatePseudoConsole.Call(
		size.pack(),
		uintptr(inRead),
		uintptr(outWrite),
		0,
		uintptr(unsafe.Pointer(&hpc)),
	)
	if hr != 0 { // anything other than S_OK
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		windows.CloseHandle(outWrite)
		return nil, fmt.Errorf("CreatePseudoConsole failed: HRESULT 0x%08x", uint32(hr))
	}

	// The ConPTY duplicates the child-side handles internally, so we release
	// our copies now and keep only the ends we read/write.
	windows.CloseHandle(inRead)
	windows.CloseHandle(outWrite)

	return &ConPTY{
		hpc:     hpc,
		inWrite: inWrite,
		outRead: outRead,
		In:      os.NewFile(uintptr(inWrite), "conpty-in"),
		Out:     os.NewFile(uintptr(outRead), "conpty-out"),
	}, nil
}

// Resize changes the pseudo console dimensions (e.g. on host window resize).
func (c *ConPTY) Resize(cols, rows int16) error {
	size := coord{X: cols, Y: rows}
	hr, _, _ := procResizePseudoConsole.Call(uintptr(c.hpc), size.pack())
	if hr != 0 {
		return fmt.Errorf("ResizePseudoConsole failed: HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// Close tears down the pseudo console and its pipes.
func (c *ConPTY) Close() {
	if c.hpc != 0 {
		procClosePseudoConsole.Call(uintptr(c.hpc))
		c.hpc = 0
	}
	if c.In != nil {
		c.In.Close()
	}
	if c.Out != nil {
		c.Out.Close()
	}
}

// Process is a child spawned inside a ConPTY.
type Process struct {
	handle windows.Handle
	PID    uint32
}

// Spawn launches commandLine attached to this pseudo console.
func (c *ConPTY) Spawn(commandLine string) (*Process, error) {
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, fmt.Errorf("alloc attribute list: %w", err)
	}
	defer attrList.Delete()

	// For PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE the attribute "value" is the HPCON
	// handle itself (cbSize == sizeof(HPCON)), not a pointer to it. Reinterpret
	// the handle's bits as an unsafe.Pointer; a plain uintptr->Pointer cast would
	// trip go vet's (here spurious) misuse check.
	if err := attrList.Update(
		procThreadAttributePseudoConsole,
		handleAsPointer(c.hpc),
		unsafe.Sizeof(c.hpc),
	); err != nil {
		return nil, fmt.Errorf("set pseudoconsole attribute: %w", err)
	}

	var si windows.StartupInfoEx
	si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
	si.ProcThreadAttributeList = attrList.List()

	cmdline, err := windows.UTF16PtrFromString(commandLine)
	if err != nil {
		return nil, fmt.Errorf("encode command line: %w", err)
	}

	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(
		nil,
		cmdline,
		nil,
		nil,
		false, // ConPTY requires bInheritHandles = FALSE
		flags,
		nil,
		nil,
		&si.StartupInfo,
		&pi,
	); err != nil {
		return nil, fmt.Errorf("CreateProcess: %w", err)
	}
	windows.CloseHandle(pi.Thread)
	return &Process{handle: pi.Process, PID: pi.ProcessId}, nil
}

// Wait blocks until the process exits and returns its exit code.
func (p *Process) Wait() (uint32, error) {
	if _, err := windows.WaitForSingleObject(p.handle, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(p.handle, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// Kill forcibly terminates the process.
func (p *Process) Kill() error {
	if p.handle == 0 {
		return nil
	}
	return windows.TerminateProcess(p.handle, 1)
}

// Close releases the process handle.
func (p *Process) Close() {
	if p.handle != 0 {
		windows.CloseHandle(p.handle)
		p.handle = 0
	}
}
