package main

import (
	"encoding/binary"
	"io"
	"sync"

	"panewright/internal/coninput"
	"panewright/internal/mux"
	"panewright/internal/winpipe"
)

// sessionPrefix namespaces pmux pipes among all named pipes on the system.
const sessionPrefix = "pmux-"

func pipeName(session string) string { return sessionPrefix + session }

// server hosts a Manager and serves one attached client at a time over a named
// pipe; new clients take over from (detach) any current one.
type server struct {
	mgr *mux.Manager
	ln  *winpipe.Listener

	mu  sync.Mutex
	cur *frameConn
}

// runServer is the entry point of the background server process:
//
//	pmux __server <session> [-f conf] [shell...]
func runServer(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	session := args[0]
	confPath, shellArgs := parseArgs(args[1:])

	if err := bindConsoleStdHandles(); err != nil {
		debugf("server: bind console std handles: %v", err)
	}

	ln, err := winpipe.Listen(pipeName(session))
	if err != nil {
		return err
	}

	cfg := loadConfig(confPath)
	if len(shellArgs) > 0 {
		// an explicit command on the CLI beats default-command from the config
		delete(cfg.Options, "default-command")
		delete(cfg.Options, "default-shell")
	}
	mgr := mux.NewManager(io.Discard, 80, 24, shellCommand(shellArgs), cfg)
	mgr.SetSession(session)
	if err := mgr.NewWindow(); err != nil {
		return err
	}

	mux.DebugLog = func(s string) { debugf("mgr: %s", s) }
	debugf("server: started session=%s", session)
	s := &server{mgr: mgr, ln: ln}
	mgr.SetDetachHook(s.detachCurrent)
	go s.acceptLoop()

	<-mgr.Done()
	return nil
}

func (s *server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serveClient(newFrameConn(conn))
	}
}

func (s *server) serveClient(link *frameConn) {
	// New client takes over: detach any current client.
	s.mu.Lock()
	old := s.cur
	s.cur = link
	s.mu.Unlock()
	if old != nil {
		old.sendCtl(msgDetach)
		old.close()
	}

	armed := false
	for {
		mt, payload, err := link.readFrame()
		if err != nil {
			break
		}
		debugf("serve: frame type=%d", mt)
		switch mt {
		case msgSize:
			cols := int(binary.LittleEndian.Uint16(payload[0:]))
			rows := int(binary.LittleEndian.Uint16(payload[2:]))
			debugf("serve: size %dx%d -> attach", cols, rows)
			s.mgr.Attach(link, cols, rows)
		case msgInput:
			ev := decodeEvent(payload)
			debugf("serve: input type=%d vk=%d char=%d down=%v ctrl=%v", ev.Type, ev.VK, ev.Char, ev.KeyDown, ev.Ctrl)
			switch ev.Type {
			case coninput.EventKey:
				if ev.KeyDown {
					armed = handleKey(s.mgr, ev, armed)
				}
			case coninput.EventMouse:
				handleMouse(s.mgr, ev)
			}
		case msgKill:
			s.mgr.Close()
			link.close()
			return
		}
	}

	// Client disconnected (pipe closed).
	s.mu.Lock()
	if s.cur == link {
		s.cur = nil
		s.mgr.Detach()
	}
	s.mu.Unlock()
	link.close()
}

// detachCurrent is the Manager's detach hook (prefix d): drop the current
// client without stopping the shells.
func (s *server) detachCurrent() {
	s.mu.Lock()
	link := s.cur
	s.cur = nil
	s.mu.Unlock()
	if link != nil {
		link.sendCtl(msgDetach)
		link.close()
	}
	s.mgr.Detach()
}
