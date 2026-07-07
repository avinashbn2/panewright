package mux

import (
	"testing"

	"panewright/internal/vt"
)

func TestColorSGR(t *testing.T) {
	cases := []struct {
		name string
		bg   bool
		want string
	}{
		{"red", false, "31"},
		{"green", true, "42"},
		{"brightblue", false, "94"},
		{"colour200", false, "38;5;200"},
		{"colour200", true, "48;5;200"},
		{"default", false, ""},
		{"bogus", false, ""},
	}
	for _, c := range cases {
		if got := colorSGR(c.name, c.bg); got != c.want {
			t.Errorf("colorSGR(%q, bg=%v) = %q, want %q", c.name, c.bg, got, c.want)
		}
	}
}

func TestBuildStatusAttr(t *testing.T) {
	a, ok := buildStatusAttr(map[string]string{"status-style": "bg=blue,fg=white,bold"})
	if !ok {
		t.Fatal("expected ok")
	}
	if a.BG != "44" || a.FG != "37" || !a.Bold {
		t.Fatalf("attr = %+v", a)
	}
}

func TestBuildStatusAttrStandalone(t *testing.T) {
	a, ok := buildStatusAttr(map[string]string{"status-bg": "green", "status-fg": "black"})
	if !ok || a.BG != "42" || a.FG != "30" {
		t.Fatalf("attr = %+v ok=%v", a, ok)
	}
}

func TestBuildStatusAttrNone(t *testing.T) {
	if _, ok := buildStatusAttr(map[string]string{}); ok {
		t.Fatal("expected not ok for empty options")
	}
}

var _ = vt.Attr{}
