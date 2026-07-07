package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"panewright/internal/coninput"
)

// The client and server exchange length-delimited frames over one duplex pipe.
// Client -> server: msgInput, msgSize, msgKill. Server -> client: msgOutput,
// msgDetach.
type msgType byte

const (
	msgInput  msgType = 1
	msgSize   msgType = 2
	msgOutput msgType = 3
	msgDetach msgType = 4
	msgKill   msgType = 5
)

// eventSize is the fixed serialized size of a coninput.Event.
const eventSize = 27

func b2i(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func encodeEventInto(b []byte, ev coninput.Event) {
	b[0] = byte(ev.Type)
	b[1] = b2i(ev.KeyDown)
	b[2] = b2i(ev.Ctrl)
	b[3] = b2i(ev.Alt)
	b[4] = b2i(ev.Shift)
	binary.LittleEndian.PutUint16(b[5:], ev.VK)
	binary.LittleEndian.PutUint32(b[7:], uint32(ev.Char))
	binary.LittleEndian.PutUint32(b[11:], uint32(int32(ev.X)))
	binary.LittleEndian.PutUint32(b[15:], uint32(int32(ev.Y)))
	binary.LittleEndian.PutUint32(b[19:], ev.ButtonState)
	binary.LittleEndian.PutUint32(b[23:], ev.MouseFlags)
}

func decodeEvent(b []byte) coninput.Event {
	return coninput.Event{
		Type:        coninput.EventType(b[0]),
		KeyDown:     b[1] != 0,
		Ctrl:        b[2] != 0,
		Alt:         b[3] != 0,
		Shift:       b[4] != 0,
		VK:          binary.LittleEndian.Uint16(b[5:]),
		Char:        rune(binary.LittleEndian.Uint32(b[7:])),
		X:           int(int32(binary.LittleEndian.Uint32(b[11:]))),
		Y:           int(int32(binary.LittleEndian.Uint32(b[15:]))),
		ButtonState: binary.LittleEndian.Uint32(b[19:]),
		MouseFlags:  binary.LittleEndian.Uint32(b[23:]),
	}
}

// frameConn wraps a connection with framed reads/writes. Writes are serialized
// by a mutex (multiple goroutines may send); a single goroutine reads.
type frameConn struct {
	c   io.ReadWriteCloser
	wmu sync.Mutex
}

func newFrameConn(c io.ReadWriteCloser) *frameConn { return &frameConn{c: c} }

// Write frames p as server output (implements io.Writer for the compositor).
func (f *frameConn) Write(p []byte) (int, error) {
	debugf("frameConn.Write enter %d", len(p))
	f.wmu.Lock()
	defer f.wmu.Unlock()
	debugf("frameConn.Write got lock")
	var hdr [5]byte
	hdr[0] = byte(msgOutput)
	binary.LittleEndian.PutUint32(hdr[1:], uint32(len(p)))
	if _, err := f.c.Write(hdr[:]); err != nil {
		debugf("out: hdr err %v", err)
		return 0, err
	}
	if _, err := f.c.Write(p); err != nil {
		debugf("out: body err %v", err)
		return 0, err
	}
	debugf("out: %d bytes", len(p))
	return len(p), nil
}

func (f *frameConn) sendInput(ev coninput.Event) error {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	buf := make([]byte, 1+eventSize)
	buf[0] = byte(msgInput)
	encodeEventInto(buf[1:], ev)
	_, err := f.c.Write(buf)
	return err
}

func (f *frameConn) sendSize(cols, rows int) error {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	var b [5]byte
	b[0] = byte(msgSize)
	binary.LittleEndian.PutUint16(b[1:], uint16(cols))
	binary.LittleEndian.PutUint16(b[3:], uint16(rows))
	_, err := f.c.Write(b[:])
	return err
}

func (f *frameConn) sendCtl(t msgType) error {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	_, err := f.c.Write([]byte{byte(t)})
	return err
}

func (f *frameConn) close() error { return f.c.Close() }

// readFrame reads the next frame's type and payload. Only one goroutine per
// connection should call this.
func (f *frameConn) readFrame() (msgType, []byte, error) {
	var t [1]byte
	if _, err := io.ReadFull(f.c, t[:]); err != nil {
		return 0, nil, err
	}
	switch msgType(t[0]) {
	case msgInput:
		buf := make([]byte, eventSize)
		if _, err := io.ReadFull(f.c, buf); err != nil {
			return 0, nil, err
		}
		return msgInput, buf, nil
	case msgSize:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(f.c, buf); err != nil {
			return 0, nil, err
		}
		return msgSize, buf, nil
	case msgOutput:
		var l [4]byte
		if _, err := io.ReadFull(f.c, l[:]); err != nil {
			return 0, nil, err
		}
		n := binary.LittleEndian.Uint32(l[:])
		buf := make([]byte, n)
		if _, err := io.ReadFull(f.c, buf); err != nil {
			return 0, nil, err
		}
		return msgOutput, buf, nil
	case msgDetach, msgKill:
		return msgType(t[0]), nil, nil
	}
	return 0, nil, fmt.Errorf("bad frame type %d", t[0])
}
