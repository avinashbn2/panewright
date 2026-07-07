package main

import (
	"io"
	"os"
	"sync"
	"time"

	"panewright/internal/coninput"
	"panewright/internal/hostterm"
)

// runClient attaches the host console to a server connection: it forwards
// console input events to the server and writes the server's rendered output to
// stdout, until the server detaches or the link breaks.
func runClient(link *frameConn) error {
	cols, rows, err := hostterm.Size()
	if err != nil {
		return err
	}
	state, err := hostterm.MakeRaw()
	if err != nil {
		return err
	}
	defer state.Restore()

	out := os.Stdout
	link.sendSize(int(cols), int(rows))
	debugf("CLIENT: sent size %dx%d, starting reader", cols, rows)

	done := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(done) }) }

	// Server -> stdout.
	go func() {
		defer finish()
		for {
			mt, payload, err := link.readFrame()
			if err != nil {
				debugf("CLIENT: read err %v", err)
				return
			}
			debugf("CLIENT: frame %d", mt)
			switch mt {
			case msgOutput:
				out.Write(payload)
			case msgDetach:
				return
			}
		}
	}()

	// Watch for host resize.
	go func() {
		c, r := cols, rows
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
			}
			nc, nr, err := hostterm.Size()
			if err != nil {
				return
			}
			if nc != c || nr != r {
				c, r = nc, nr
				if link.sendSize(int(nc), int(nr)) != nil {
					return
				}
			}
		}
	}()

	// Console input -> server.
	go func() {
		rdr, err := coninput.Open()
		if err != nil {
			finish()
			return
		}
		defer rdr.Close()
		for {
			select {
			case <-done:
				return
			default:
			}
			evs, err := rdr.Read()
			if err != nil {
				finish()
				return
			}
			for _, ev := range evs {
				if ev.Type == coninput.EventResize {
					if c, r, err := hostterm.Size(); err == nil {
						link.sendSize(int(c), int(r))
					}
					continue
				}
				if link.sendInput(ev) != nil {
					finish()
					return
				}
			}
		}
	}()

	<-done
	link.close()
	io.WriteString(out, "\x1b[r\x1b[?25h\x1b[0m\x1b[2J\x1b[H")
	return nil
}
