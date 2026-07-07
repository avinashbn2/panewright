// Command pmux is a native Windows terminal multiplexer built on ConPTY.
//
// pmux runs a tmux-style server/client split: a detached background server
// process owns the sessions (each a set of windows; each window a tree of
// panes, each pane a shell in its own pseudo console), and a thin client
// attaches the host console to it. Detaching leaves the shells running in the
// server so a later `pmux attach` reconnects to them.
//
// Usage:
//
//	pmux [-f config] [command...]   attach the default session (creating it)
//	pmux new -s NAME [command...]   create and attach a named session
//	pmux attach [-t NAME]           attach an existing session
//	pmux ls                         list sessions
//	pmux kill-server [-t NAME]      stop a session's server
//
// Inside, the prefix key (Ctrl-B by default) opens a one-key command layer and
// ~/.tmux.conf is honored for the prefix, bindings, and appearance.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"panewright/internal/config"
	"panewright/internal/winpipe"
)

const defaultSession = "main"

var errUsage = errors.New("usage: pmux [-f config] [command...] | new -s NAME | attach -t NAME | ls | kill-server [-t NAME]")

// dbgFile, when PMUX_DEBUG names a path, receives diagnostic traces.
var dbgFile *os.File

func init() {
	if p := os.Getenv("PMUX_DEBUG"); p != "" {
		dbgFile, _ = os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	}
}

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "pmux: %v\n", err)
		os.Exit(1)
	}
}

func dispatch(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "__server":
			return runServer(args[1:])
		case "ls", "list-sessions":
			return listSessions()
		case "attach", "attach-session", "a":
			return attachCmd(args[1:])
		case "new", "new-session":
			return newCmd(args[1:])
		case "kill-server", "kill-session":
			return killCmd(args[1:])
		}
	}
	return defaultCmd(args)
}

// defaultCmd attaches the default session, creating its server (hosting any
// given command) if it isn't running.
func defaultCmd(args []string) error {
	confPath, shellArgs := parseArgs(args)
	return ensureAndAttach(defaultSession, confPath, shellArgs, true)
}

// newCmd creates (or attaches, if it already exists) a named session.
func newCmd(args []string) error {
	name, confPath, shellArgs := parseTarget(args, "-s")
	if name == "" {
		name = defaultSession
	}
	return ensureAndAttach(name, confPath, shellArgs, true)
}

// attachCmd attaches an existing named session, erroring if it isn't running.
func attachCmd(args []string) error {
	name, confPath, shellArgs := parseTarget(args, "-t")
	if name == "" {
		name = defaultSession
	}
	if !sessionExists(name) {
		return fmt.Errorf("no session %q", name)
	}
	return ensureAndAttach(name, confPath, shellArgs, false)
}

func ensureAndAttach(name, confPath string, shellArgs []string, create bool) error {
	if !sessionExists(name) {
		if !create {
			return fmt.Errorf("no session %q", name)
		}
		if err := spawnServer(name, confPath, shellArgs); err != nil {
			return err
		}
		if err := waitForSession(name); err != nil {
			return err
		}
	}
	conn, err := dialRetry(name)
	if err != nil {
		return err
	}
	return runClient(newFrameConn(conn))
}

// listSessions prints the live session names.
func listSessions() error {
	names := liveSessions()
	if len(names) == 0 {
		fmt.Println("no sessions")
		return nil
	}
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

// killCmd stops a session's server (and its shells).
func killCmd(args []string) error {
	name, _, _ := parseTarget(args, "-t")
	if name == "" {
		name = defaultSession
	}
	if !sessionExists(name) {
		return fmt.Errorf("no session %q", name)
	}
	conn, err := winpipe.Dial(pipeName(name))
	if err != nil {
		return err
	}
	link := newFrameConn(conn)
	link.sendCtl(msgKill)
	link.close()
	return nil
}

// ---- session discovery & server spawning ----

func liveSessions() []string {
	names, err := winpipe.List(sessionPrefix)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, strings.TrimPrefix(n, sessionPrefix))
	}
	return out
}

func sessionExists(name string) bool {
	for _, n := range liveSessions() {
		if n == name {
			return true
		}
	}
	return false
}

// spawnServer launches a detached background server process for the session.
func spawnServer(name, confPath string, shellArgs []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"__server", name}
	if confPath != "" {
		args = append(args, "-f", confPath)
	}
	args = append(args, shellArgs...)
	cmd := exec.Command(exe, args...)
	cmd.Env = os.Environ()
	if p := os.Getenv("PMUX_DEBUG"); p != "" {
		cmd.Env = append(cmd.Env, "PMUX_DEBUG="+p+".server")
	}
	// The server needs its own (invisible) console to host ConPTY children.
	// CREATE_NO_WINDOW gives it a windowless console. CREATE_NEW_CONSOLE +
	// HideWindow does NOT work here: combined with the STARTF_USESTDHANDLES that
	// Go's exec always sets, shells spawned into the server's pseudo consoles
	// exit immediately with code 0. It outlives this client because we don't
	// wait on it and release its handle below.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// waitForSession blocks until the server's pipe appears (or times out).
func waitForSession(name string) error {
	for i := 0; i < 100; i++ {
		if sessionExists(name) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("server for %q did not start", name)
}

// dialRetry connects to a session, retrying briefly while the server is still
// setting up its first pipe instance.
func dialRetry(name string) (*winpipe.Conn, error) {
	var lastErr error
	for i := 0; i < 100; i++ {
		conn, err := winpipe.Dial(pipeName(name))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return nil, lastErr
}

// ---- argument parsing ----

// parseArgs pulls an optional "-f <config>" out of args; the rest is the
// command to host.
func parseArgs(args []string) (confPath string, shellArgs []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == "-f" && i+1 < len(args) {
			confPath = args[i+1]
			i++
			continue
		}
		shellArgs = append(shellArgs, args[i])
	}
	return confPath, shellArgs
}

// parseTarget pulls a name flag (e.g. -t or -s) and -f out of args; the rest is
// the command to host.
func parseTarget(args []string, nameFlag string) (name, confPath string, shellArgs []string) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == nameFlag && i+1 < len(args):
			name = args[i+1]
			i++
		case args[i] == "-f" && i+1 < len(args):
			confPath = args[i+1]
			i++
		default:
			shellArgs = append(shellArgs, args[i])
		}
	}
	return name, confPath, shellArgs
}

// loadConfig loads ~/.pmux.conf (or, as a tmux-compatibility fallback,
// ~/.tmux.conf) if present, then an explicit -f file.
func loadConfig(explicit string) *config.Config {
	cfg := config.New()
	if home, err := os.UserHomeDir(); err == nil {
		if err := cfg.Load(home + `\.pmux.conf`); err != nil {
			_ = cfg.Load(home + `\.tmux.conf`)
		}
	}
	if explicit != "" {
		if err := cfg.Load(explicit); err != nil {
			fmt.Fprintf(os.Stderr, "pmux: config %s: %v\n", explicit, err)
		}
	}
	for _, w := range cfg.Warnings {
		debugf("config: %s", w)
	}
	return cfg
}

// shellCommand picks the command line to host: explicit args win, otherwise
// PowerShell, falling back to %COMSPEC% (cmd.exe).
func shellCommand(args []string) string {
	if len(args) > 0 {
		return strings.Join(args, " ")
	}
	if _, err := os.Stat(os.Getenv("SystemRoot") + `\System32\WindowsPowerShell\v1.0\powershell.exe`); err == nil {
		return "powershell.exe"
	}
	if c := os.Getenv("COMSPEC"); c != "" {
		return c
	}
	return "cmd.exe"
}

func debugf(format string, args ...any) {
	if dbgFile == nil {
		return
	}
	fmt.Fprintf(dbgFile, format+"\n", args...)
}
