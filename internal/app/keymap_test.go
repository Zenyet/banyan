package app

import (
	"runtime"
	"testing"
)

func TestDefaultShortcutsParseAndAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range commands() {
		if c.DefaultKeys == "" {
			continue
		}
		if accelerator(c.DefaultKeys) == "" {
			t.Errorf("%s: %q does not parse", c.ID, c.DefaultKeys)
		}
		if seen[c.DefaultKeys] {
			t.Errorf("%s is bound twice", c.DefaultKeys)
		}
		seen[c.DefaultKeys] = true
	}
}

func TestReadsKeysWrittenByHand(t *testing.T) {
	cases := map[string]string{
		"secondary-shift-K":       "CmdOrCtrl+Shift+K",
		"ctrl-alt-p":              "Ctrl+Alt+P",
		"secondary-k secondary-c": "CmdOrCtrl+K",
		"  ":                      "",
		"hyper-k":                 "",
		"secondary-,":             "CmdOrCtrl+,",
	}
	for keys, want := range cases {
		if got := accelerator(keys); got != want {
			t.Errorf("accelerator(%q) = %q, want %q", keys, got, want)
		}
	}
	if runtime.GOOS == "darwin" {
		if got := displayKeys("secondary-shift-k"); got != "⇧⌘K" {
			t.Errorf("display = %q", got)
		}
		if got := displayKeys("alt-secondary-enter"); got != "⌥⌘↩" {
			t.Errorf("display = %q", got)
		}
	} else if got := displayKeys("secondary-shift-k"); got != "Ctrl+Shift+K" {
		t.Errorf("display = %q", got)
	}
}
