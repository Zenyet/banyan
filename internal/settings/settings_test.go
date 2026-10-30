package settings

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Zenyet/banyan/internal/git"
)

func TestMessagesRoundTrip(t *testing.T) {
	for _, message := range []string{"Subject", "Subject\n\nBody line\nC:\\path\\n", "trailing \\"} {
		escaped := escape(message)
		if strings.Contains(escaped, "\n") {
			t.Errorf("%q kept a newline", escaped)
		}
		if got := unescape(escaped); got != message {
			t.Errorf("unescape(escape(%q)) = %q", message, got)
		}
	}
}

func TestMatchesProtectedBranches(t *testing.T) {
	s := Default()
	s.ProtectedBranches = "main, release/* *-stable"
	for _, name := range []string{"main", "origin/main", "refs/heads/main", "release/1.0", "origin/release/2", "v1-stable"} {
		if !s.IsProtected(name) {
			t.Errorf("%s should be protected", name)
		}
	}
	for _, name := range []string{"mainline", "feature/main-fix", "releases", "stable"} {
		if s.IsProtected(name) {
			t.Errorf("%s should not be protected", name)
		}
	}
	s.ProtectedBranches = ""
	if s.IsProtected("main") {
		t.Error("nothing is protected without patterns")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s := Settings{
		Theme:             ThemeDark,
		FontSize:          14,
		RelativeDates:     false,
		GitPath:           "/opt/homebrew/bin/git",
		UpdateMethod:      git.UpdateFastForwardOnly,
		AutoFetchMinutes:  15,
		UnifiedDiff:       true,
		IgnoreWhitespace:  true,
		Language:          "zh",
		LogPageSize:       2000,
		ProtectedBranches: "main release/*",
		OpenInNewWindow:   true,
		Recent:            []string{"/tmp/repo"},
		Session:           []string{"/tmp/a", "/tmp/b"},
		Messages:          []string{"Fix\n\nDetails"},
		Shortcuts:         []Shortcut{{"push", "secondary-alt-p"}, {"fetch", ""}},
	}
	if got := parse(s.text()); !reflect.DeepEqual(got, s) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, s)
	}
	// Files written by earlier versions.
	old := parse("theme=light\nupdate=merge\nrecent=/tmp/a\nfont_size=16\n")
	if old.Theme != ThemeLight || old.FontSize != 13 || old.LogPageSize != 5000 {
		t.Errorf("old file: %+v", old)
	}
	d := Default()
	if d.GitProgram() != "git" {
		t.Errorf("GitProgram() = %q", d.GitProgram())
	}
}
