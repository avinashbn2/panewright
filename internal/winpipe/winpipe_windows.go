// Package winpipe is a thin wrapper over Windows named pipes used for the panewright
// server/client link. The server Listens and Accepts connections; clients Dial.
//
// Pipes are opened in OVERLAPPED mode so a connection can be read and written
// concurrently from different goroutines. With synchronous handles a blocked
// ReadFile would serialize with a concurrent WriteFile on the same handle and
// deadlock (the server reads input while writing output on one connection).
package winpipe

import (
	"io"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	pipeAccessDuplex   = 0x00000003
	fileFlagOverlapped = 0x40000000
	pipeTypeByte       = 0x00000000
	pipeReadmodeByte   = 0x00000000
	pipeWait           = 0x00000000
	pipeUnlimited      = 255
	bufSize            = 64 * 1024

	errPipeConnected = syscall.Errno(535)
)

// Path returns the full pipe path for a bare name.
func Path(name string) string { return `\\.\pipe\` + name }

// Conn is one end of a named-pipe connection. Reads and writes each carry their
// own event so they can overlap.
type Conn struct {
	h      windows.Handle
	server bool
	rEvent windows.Handle
	wEvent windows.Handle
}

func newConn(h windows.Handle, server bool) (*Conn, error) {
	rEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	wEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(rEvent)
		windows.CloseHandle(h)
		return nil, err
	}
	return &Conn{h: h, server: server, rEvent: rEvent, wEvent: wEvent}, nil
}

// Read implements io.Reader.
func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var ov windows.Overlapped
	ov.HEvent = c.rEvent
	windows.ResetEvent(c.rEvent)
	var done uint32
	err := windows.ReadFile(c.h, p, &done, &ov)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(c.h, &ov, &done, true)
	}
	if err != nil {
		if isEOF(err) {
			return int(done), io.EOF
		}
		return int(done), err
	}
	if done == 0 {
		return 0, io.EOF
	}
	return int(done), nil
}

// Write implements io.Writer.
func (c *Conn) Write(p []byte) (int, error) {
	var ov windows.Overlapped
	ov.HEvent = c.wEvent
	windows.ResetEvent(c.wEvent)
	var done uint32
	err := windows.WriteFile(c.h, p, &done, &ov)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(c.h, &ov, &done, true)
	}
	if err != nil {
		return int(done), err
	}
	return int(done), nil
}

// Close releases the connection and its events.
func (c *Conn) Close() error {
	if c.server {
		windows.FlushFileBuffers(c.h)
		windows.DisconnectNamedPipe(c.h)
	}
	windows.CloseHandle(c.rEvent)
	windows.CloseHandle(c.wEvent)
	return windows.CloseHandle(c.h)
}

func isEOF(err error) bool {
	return err == windows.ERROR_BROKEN_PIPE || err == windows.ERROR_PIPE_NOT_CONNECTED || err == windows.ERROR_NO_DATA
}

// Listener accepts client connections on a named pipe.
type Listener struct {
	name *uint16
}

// Listen prepares a listener for the given bare pipe name.
func Listen(name string) (*Listener, error) {
	p, err := windows.UTF16PtrFromString(Path(name))
	if err != nil {
		return nil, err
	}
	return &Listener{name: p}, nil
}

// Accept creates a fresh pipe instance and blocks until a client connects.
func (l *Listener) Accept() (*Conn, error) {
	h, err := windows.CreateNamedPipe(
		l.name,
		pipeAccessDuplex|fileFlagOverlapped,
		pipeTypeByte|pipeReadmodeByte|pipeWait,
		pipeUnlimited,
		bufSize, bufSize,
		0, nil,
	)
	if err != nil {
		return nil, err
	}
	conn, err := newConn(h, true)
	if err != nil {
		return nil, err
	}
	var ov windows.Overlapped
	ov.HEvent = conn.rEvent
	windows.ResetEvent(conn.rEvent)
	err = windows.ConnectNamedPipe(h, &ov)
	switch err {
	case nil, errPipeConnected:
		// connected
	case windows.ERROR_IO_PENDING:
		var done uint32
		if err := windows.GetOverlappedResult(h, &ov, &done, true); err != nil {
			conn.Close()
			return nil, err
		}
	default:
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// Dial connects to an existing server pipe by bare name.
func Dial(name string) (*Conn, error) {
	p, err := windows.UTF16PtrFromString(Path(name))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil,
		windows.OPEN_EXISTING,
		fileFlagOverlapped, 0,
	)
	if err != nil {
		return nil, err
	}
	return newConn(h, false)
}

// List returns the bare names of all named pipes beginning with prefix.
func List(prefix string) ([]string, error) {
	p, err := windows.UTF16PtrFromString(`\\.\pipe\*`)
	if err != nil {
		return nil, err
	}
	var fd windows.Win32finddata
	h, err := windows.FindFirstFile(p, &fd)
	if err != nil {
		return nil, err
	}
	defer windows.FindClose(h)
	var out []string
	for {
		name := windows.UTF16ToString(fd.FileName[:])
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
		if err := windows.FindNextFile(h, &fd); err != nil {
			break
		}
	}
	return out, nil
}
