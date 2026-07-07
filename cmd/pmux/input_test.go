package main

import (
	"bytes"
	"testing"

	"panewright/internal/config"
	"panewright/internal/coninput"
)

func TestKeyOf(t *testing.T) {
	cases := []struct {
		name string
		ev   coninput.Event
		want config.Key
		ok   bool
	}{
		{"ctrl-b", coninput.Event{VK: 'B', Char: 2, Ctrl: true}, config.Key{Rune: 'b', Ctrl: true}, true},
		{"plain a", coninput.Event{VK: 'A', Char: 'a'}, config.Key{Rune: 'a'}, true},
		{"arrow up", coninput.Event{VK: 0x26}, config.Key{Rune: config.KeyUp}, true},
		{"alt-x", coninput.Event{VK: 'X', Char: 'x', Alt: true}, config.Key{Rune: 'x', Alt: true}, true},
		{"bare ctrl", coninput.Event{VK: 0x11, Char: 0, Ctrl: true}, config.Key{}, false},
	}
	for _, c := range cases {
		got, ok := keyOf(c.ev)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: keyOf = %+v,%v want %+v,%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestEncodeKey(t *testing.T) {
	cases := []struct {
		name string
		ev   coninput.Event
		want []byte
	}{
		{"letter", coninput.Event{Char: 'a'}, []byte("a")},
		{"enter", coninput.Event{VK: 0x0D, Char: '\r'}, []byte{'\r'}},
		{"ctrl-c", coninput.Event{VK: 'C', Char: 3, Ctrl: true}, []byte{3}},
		{"alt-a", coninput.Event{Char: 'a', Alt: true}, []byte{0x1b, 'a'}},
		{"arrow up", coninput.Event{VK: 0x26}, []byte("\x1b[A")},
		{"backspace", coninput.Event{VK: 0x08, Char: 8}, []byte{0x7f}},
		{"f1", coninput.Event{VK: 0x70}, []byte("\x1bOP")},
		{"bare modifier", coninput.Event{VK: 0x10, Char: 0}, nil},
		{"utf8 rune", coninput.Event{Char: 0x00e9}, []byte("é")},
	}
	for _, c := range cases {
		got := encodeKey(c.ev)
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s: encodeKey = % x, want % x", c.name, got, c.want)
		}
	}
}
