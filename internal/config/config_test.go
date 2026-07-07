package config

import "testing"

func TestParseKeyCtrl(t *testing.T) {
	k, ok := ParseKey("C-a")
	if !ok || k != (Key{Rune: 'a', Ctrl: true}) {
		t.Fatalf("C-a => %+v ok=%v", k, ok)
	}
}

func TestParseKeyCtrlUppercaseNormalized(t *testing.T) {
	k, _ := ParseKey("C-B")
	if k != (Key{Rune: 'b', Ctrl: true}) {
		t.Fatalf("C-B => %+v", k)
	}
}

func TestParseKeyAltAndSymbols(t *testing.T) {
	if k, _ := ParseKey("M-x"); k != (Key{Rune: 'x', Alt: true}) {
		t.Fatalf("M-x => %+v", k)
	}
	if k, _ := ParseKey("%"); k != (Key{Rune: '%'}) {
		t.Fatalf("%% => %+v", k)
	}
}

func TestParseKeyNamed(t *testing.T) {
	if k, _ := ParseKey("Up"); k.Rune != KeyUp {
		t.Fatalf("Up => %+v", k)
	}
	if k, _ := ParseKey("Space"); k.Rune != ' ' {
		t.Fatalf("Space => %+v", k)
	}
}

func TestSetPrefix(t *testing.T) {
	c := New()
	c.ParseLine("set -g prefix C-a")
	if !c.HasPrefix || c.Prefix != (Key{Rune: 'a', Ctrl: true}) {
		t.Fatalf("prefix => %+v has=%v", c.Prefix, c.HasPrefix)
	}
}

func TestSetOption(t *testing.T) {
	c := New()
	c.ParseLine("set -g status off")
	c.ParseLine("set -g status-style bg=green,fg=black")
	if c.Options["status"] != "off" {
		t.Fatalf("status = %q", c.Options["status"])
	}
	if c.Options["status-style"] != "bg=green,fg=black" {
		t.Fatalf("status-style = %q", c.Options["status-style"])
	}
}

func TestBind(t *testing.T) {
	c := New()
	c.ParseLine(`bind | split-window -h`)
	if len(c.Binds) != 1 {
		t.Fatalf("binds = %d", len(c.Binds))
	}
	b := c.Binds[0]
	if b.Key.Rune != '|' || len(b.Cmds) != 1 {
		t.Fatalf("bind = %+v", b)
	}
	if cmd := b.Cmds[0]; cmd.Name != "split-window" || len(cmd.Args) != 1 || cmd.Args[0] != "-h" {
		t.Fatalf("bind cmd = %+v", cmd)
	}
}

func TestBindCommandSequence(t *testing.T) {
	c := New()
	c.ParseLine(`bind e split-window -h \; select-pane -L`)
	if len(c.Binds) != 1 || len(c.Binds[0].Cmds) != 2 {
		t.Fatalf("binds = %+v", c.Binds)
	}
	if c.Binds[0].Cmds[1].Name != "select-pane" {
		t.Fatalf("second cmd = %+v", c.Binds[0].Cmds[1])
	}
}

func TestBindRoot(t *testing.T) {
	c := New()
	c.ParseLine(`bind -n M-Left select-pane -L`)
	b := c.Binds[0]
	if !b.Root || b.Key != (Key{Rune: KeyLeft, Alt: true}) {
		t.Fatalf("bind -n => %+v", b)
	}
}

func TestUnbind(t *testing.T) {
	c := New()
	c.ParseLine("unbind C-b")
	if len(c.Unbinds) != 1 || c.Unbinds[0].Key != (Key{Rune: 'b', Ctrl: true}) {
		t.Fatalf("unbinds = %+v", c.Unbinds)
	}
}

func TestTokenizeQuotesAndComments(t *testing.T) {
	got := Tokenize(`bind c "new-window" # make a window`)
	want := []string{"bind", "c", "new-window"}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCommentLineIgnored(t *testing.T) {
	c := New()
	c.ParseLine("# just a comment")
	if len(c.Warnings) != 0 {
		t.Fatalf("comment produced warnings: %v", c.Warnings)
	}
}
