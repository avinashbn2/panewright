// Package config parses tmux-style configuration files into data the
// multiplexer can apply: a prefix key, options, and key bindings. It aims to
// accept the common subset of real tmux.conf syntax and ignore (with a
// warning) anything it doesn't understand, so existing configs load without
// erroring.
package config

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Special key runes (private-use area) for keys with no character. The input
// decoder maps Windows virtual key codes to these so bindings can target them.
const (
	KeyUp     = rune(0xE000)
	KeyDown   = rune(0xE001)
	KeyLeft   = rune(0xE002)
	KeyRight  = rune(0xE003)
	KeyHome   = rune(0xE004)
	KeyEnd    = rune(0xE005)
	KeyPgUp   = rune(0xE006)
	KeyPgDn   = rune(0xE007)
	KeyInsert = rune(0xE008)
	KeyDelete = rune(0xE009)
	KeyF1     = rune(0xE010) // F1..F12 are contiguous: KeyF1+n
)

// Key is a normalized keypress: a base rune plus modifier flags. Shift is only
// meaningful for special (non-character) keys; for characters the shifted rune
// itself is the key.
type Key struct {
	Rune  rune
	Ctrl  bool
	Alt   bool
	Shift bool
}

func (k Key) String() string {
	var b strings.Builder
	if k.Ctrl {
		b.WriteString("C-")
	}
	if k.Alt {
		b.WriteString("M-")
	}
	if k.Shift {
		b.WriteString("S-")
	}
	if name, ok := keyNames[k.Rune]; ok {
		b.WriteString(name)
	} else {
		b.WriteRune(k.Rune)
	}
	return b.String()
}

// Command is a parsed tmux command line: a name and its arguments.
type Command struct {
	Name string
	Args []string
}

// Binding maps a key to one or more commands (a `\;` sequence). Root bindings
// (bind -n) fire without the prefix.
type Binding struct {
	Key  Key
	Root bool
	Cmds []Command
}

// Config is the accumulated result of parsing one or more files.
type Config struct {
	Prefix    Key
	HasPrefix bool
	Options   map[string]string
	Binds     []Binding
	Unbinds   []Binding // Cmds unused; Key+Root identify what to remove
	Warnings  []string

	depth int // source-file nesting depth
}

// New returns an empty config with sensible defaults.
func New() *Config {
	return &Config{Options: map[string]string{}}
}

// Load reads and parses a config file, merging into c.
func (c *Config) Load(path string) error {
	if c.depth > 10 {
		c.warnf("source-file: too deeply nested, skipping %s", path)
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	c.depth++
	defer func() { c.depth-- }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var pending string
	skipping := 0 // %if nesting depth being skipped
	for sc.Scan() {
		line := sc.Text()
		// line continuation
		if strings.HasSuffix(line, "\\") && !strings.HasSuffix(line, "\\\\") {
			pending += strings.TrimSuffix(line, "\\") + " "
			continue
		}
		full := pending + line
		pending = ""
		trimmed := strings.TrimSpace(full)
		// %if/%elif/%else/%endif blocks (format conditionals) are not
		// evaluated; the whole block is skipped with a warning.
		if strings.HasPrefix(trimmed, "%if") {
			if skipping == 0 {
				c.warnf("%%if blocks are not supported; skipping block")
			}
			skipping++
			continue
		}
		if skipping > 0 {
			if strings.HasPrefix(trimmed, "%endif") {
				skipping--
			}
			continue
		}
		c.ParseLine(full)
	}
	return sc.Err()
}

func (c *Config) warnf(format string, args ...any) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(format, args...))
}

// ParseLine parses one config line (which may hold several `;`-separated
// commands) and merges the result into c.
func (c *Config) ParseLine(line string) {
	for _, toks := range SplitCommands(Tokenize(line)) {
		c.parseCommand(toks)
	}
}

// ParseTokens parses a single already-tokenized command into c (used for
// runtime set/bind/unbind commands).
func (c *Config) ParseTokens(toks []string) {
	c.parseCommand(toks)
}

func (c *Config) parseCommand(toks []string) {
	if len(toks) == 0 {
		return
	}
	switch toks[0] {
	case "set", "set-option", "setw", "set-window-option", "setenv", "set-environment":
		c.parseSet(toks[1:])
	case "bind", "bind-key":
		c.parseBind(toks[1:])
	case "unbind", "unbind-key":
		c.parseUnbind(toks[1:])
	case "source", "source-file":
		c.parseSource(toks[1:])
	case "if-shell", "if":
		c.parseIfShell(toks[1:])
	case "run-shell", "run":
		// Startup shell commands are not supported (the server has no client
		// terminal at config time); accepted so configs load.
		c.warnf("run-shell at config time is ignored")
	case "display-message", "display":
		// harmless at config load time
	default:
		c.warnf("ignoring unsupported command: %s", toks[0])
	}
}

func (c *Config) parseSource(args []string) {
	quiet := false
	var paths []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			if strings.Contains(a, "q") {
				quiet = true
			}
			continue
		}
		paths = append(paths, a)
	}
	for _, p := range paths {
		if err := c.Load(ExpandHome(p)); err != nil && !quiet {
			c.warnf("source-file %s: %v", p, err)
		}
	}
}

// parseIfShell handles if-shell [-b] shell-command command [command-else].
// The condition runs through the Windows shell; exit status selects the branch.
func (c *Config) parseIfShell(args []string) {
	var rest []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue // -b/-F flags: condition still evaluated synchronously
		}
		rest = append(rest, a)
	}
	if len(rest) < 2 {
		c.warnf("if-shell: need condition and command")
		return
	}
	if RunShellCondition(rest[0]) {
		c.ParseLine(rest[1])
	} else if len(rest) > 2 {
		c.ParseLine(rest[2])
	}
}

// RunShellCondition runs cond via the Windows command shell and reports
// whether it exited zero. A hung condition is abandoned after 5 seconds.
func RunShellCondition(cond string) bool {
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}
	cmd := exec.Command(shell, "/c", cond)
	if err := cmd.Start(); err != nil {
		return false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return false
	}
}

// parseSet handles set-option [-gasqwu] name [value].
func (c *Config) parseSet(args []string) {
	appendVal, unset := false, false
	var rest []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") && len(rest) == 0 {
			// flags before the option name; -g/-s/-w/-q scope/quiet accepted
			if strings.Contains(a, "a") {
				appendVal = true
			}
			if strings.Contains(a, "u") {
				unset = true
			}
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) == 0 {
		return
	}
	name := rest[0]
	value := ""
	if len(rest) > 1 {
		value = strings.Join(rest[1:], " ")
	}
	if unset {
		delete(c.Options, name)
		return
	}
	if name == "prefix" {
		if k, ok := ParseKey(value); ok {
			c.Prefix = k
			c.HasPrefix = true
		} else {
			c.warnf("bad prefix key: %q", value)
		}
		return
	}
	if appendVal {
		c.Options[name] += value
		return
	}
	c.Options[name] = value
}

// parseBind handles bind [-n] [-r] [-T table] key command [args...], where the
// command may be a `\;`-separated sequence.
func (c *Config) parseBind(args []string) {
	root := false
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") && args[i] != ";" {
		switch args[i] {
		case "-n":
			root = true
		case "-r":
			// repeatable: accepted, not modeled
		case "-T":
			i++
			if i < len(args) && args[i] == "root" {
				root = true
			}
		}
		i++
	}
	if i >= len(args) {
		c.warnf("bind: missing key")
		return
	}
	key, ok := ParseKey(args[i])
	if !ok {
		c.warnf("bind: unrecognized key %q", args[i])
		return
	}
	i++
	if i >= len(args) {
		c.warnf("bind %s: missing command", key)
		return
	}
	cmds := ParseCommands(args[i:])
	if len(cmds) == 0 {
		c.warnf("bind %s: missing command", key)
		return
	}
	c.Binds = append(c.Binds, Binding{Key: key, Root: root, Cmds: cmds})
}

// ParseCommands splits an argument list on literal ";" tokens (written `\;` in
// config files) into a command sequence.
func ParseCommands(args []string) []Command {
	var cmds []Command
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			cmds = append(cmds, Command{Name: cur[0], Args: cur[1:]})
			cur = nil
		}
	}
	for _, a := range args {
		if a == ";" {
			flush()
			continue
		}
		cur = append(cur, a)
	}
	flush()
	return cmds
}

// parseUnbind handles unbind [-n] [-a] key.
func (c *Config) parseUnbind(args []string) {
	root := false
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] == "-n" {
			root = true
		}
		i++
	}
	if i >= len(args) {
		return
	}
	if key, ok := ParseKey(args[i]); ok {
		c.Unbinds = append(c.Unbinds, Binding{Key: key, Root: root})
	}
}

// namedKeys maps tmux key names to their rune (best-effort for matching).
var namedKeys = map[string]rune{
	"Space":  ' ',
	"Enter":  '\r',
	"Tab":    '\t',
	"BSpace": '\x7f',
	"Escape": '\x1b',
	"Up":     KeyUp,
	"Down":   KeyDown,
	"Left":   KeyLeft,
	"Right":  KeyRight,
	"Home":   KeyHome,
	"End":    KeyEnd,
	"PPage":  KeyPgUp,
	"PageUp": KeyPgUp,
	"PgUp":   KeyPgUp,
	"NPage":  KeyPgDn,
	"PageDown": KeyPgDn,
	"PgDn":   KeyPgDn,
	"IC":     KeyInsert,
	"Insert": KeyInsert,
	"DC":     KeyDelete,
	"Delete": KeyDelete,
}

// keyNames is the reverse of namedKeys for String(), preferring tmux names.
var keyNames = map[rune]string{
	' ': "Space", '\r': "Enter", '\t': "Tab", '\x7f': "BSpace", '\x1b': "Escape",
	KeyUp: "Up", KeyDown: "Down", KeyLeft: "Left", KeyRight: "Right",
	KeyHome: "Home", KeyEnd: "End", KeyPgUp: "PPage", KeyPgDn: "NPage",
	KeyInsert: "IC", KeyDelete: "DC",
}

func init() {
	for i := 0; i < 12; i++ {
		r := KeyF1 + rune(i)
		namedKeys[fmt.Sprintf("F%d", i+1)] = r
		keyNames[r] = fmt.Sprintf("F%d", i+1)
	}
}

// ParseKey parses a tmux key spec like "C-b", "M-x", "%", "Space", "F5".
func ParseKey(s string) (Key, bool) {
	var k Key
	for {
		switch {
		case strings.HasPrefix(s, "C-") || strings.HasPrefix(s, "^"):
			k.Ctrl = true
			if s[0] == '^' {
				s = s[1:]
			} else {
				s = s[2:]
			}
		case strings.HasPrefix(s, "M-"):
			k.Alt = true
			s = s[2:]
		case strings.HasPrefix(s, "S-"):
			k.Shift = true
			s = s[2:]
		default:
			goto base
		}
	}
base:
	if s == "" {
		return Key{}, false
	}
	if r, ok := namedKeys[s]; ok {
		k.Rune = r
		// Shift is only tracked for special keys; a shifted character is
		// already its own rune.
		if k.Rune < 0xE000 {
			k.Shift = false
		}
		return k, true
	}
	runes := []rune(s)
	if len(runes) == 1 {
		k.Rune = runes[0]
		k.Shift = false
		// tmux treats C-b and C-B alike; normalize control letters to lower.
		if k.Ctrl && k.Rune >= 'A' && k.Rune <= 'Z' {
			k.Rune += 'a' - 'A'
		}
		return k, true
	}
	return Key{}, false
}

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(p string) string {
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

// SplitCommands splits a token stream on top-level ";" separators (produced by
// Tokenize for unquoted, unescaped semicolons) into individual commands.
func SplitCommands(toks []string) [][]string {
	var out [][]string
	var cur []string
	for _, t := range toks {
		if t == cmdSep {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// cmdSep is the token Tokenize emits for an unquoted, unescaped `;` — a
// command separator, distinct from a literal ";" argument (written `\;`).
const cmdSep = "\x00;"

// Tokenize splits a config line into tokens, honoring single/double quotes,
// stripping unquoted # comments, translating `\;` into a literal ";" token and
// a bare `;` into a command separator. Other backslashes are left literal so
// Windows paths survive.
func Tokenize(line string) []string {
	var toks []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	started := false
	flush := func() {
		if started {
			toks = append(toks, cur.String())
			cur.Reset()
			started = false
		}
	}
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case inSingle:
			if ch == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(ch)
			}
		case inDouble:
			if ch == '"' {
				inDouble = false
			} else if ch == '\\' && i+1 < len(line) && line[i+1] == '"' {
				cur.WriteByte('"')
				i++
			} else {
				cur.WriteByte(ch)
			}
		case ch == '\'':
			inSingle = true
			started = true
		case ch == '"':
			inDouble = true
			started = true
		case ch == '\\' && i+1 < len(line) && line[i+1] == ';':
			cur.WriteByte(';')
			started = true
			i++
		case ch == ';':
			flush()
			toks = append(toks, cmdSep)
		case ch == '#' && !started:
			// comment — but only at the start of a word, so unquoted hex
			// colors (bg=#333333) and formats survive
			return toks
		case ch == '#':
			cur.WriteByte(ch)
		case ch == ' ' || ch == '\t':
			flush()
		default:
			cur.WriteByte(ch)
			started = true
		}
	}
	flush()
	return toks
}
