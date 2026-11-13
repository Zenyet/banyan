package settings

import (
	"os"
	"os/exec"
	"path/filepath"
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
		AIProvider:        "acp",
		AICommand:         "npx -y @agentclientprotocol/claude-agent-acp",
		AIBaseURL:         "http://localhost:11434/v1",
		AIModel:           "llama3.2",
		AIAPIKey:          "secret",
		AIInstructions:    "Use Conventional Commits\nand English",
		AIAllowTools:      true,
		AISubjectOnly:     true,
		AIStyle:           "conventional",
		AILanguage:        "zh",
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

// Git comes from the PATH, else on Windows from where Git for Windows
// installs, else from beside the app, where its packages bring one.
func TestFindGit(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/git", nil }
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	dir, programs := t.TempDir(), t.TempDir()
	env := func(key string) string {
		if key == "ProgramFiles" {
			return programs
		}
		return ""
	}
	touch := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := findGit(found, dir, "windows", env); got != "git" {
		t.Errorf("on the PATH: %s", got)
	}
	if got := findGit(missing, dir, "windows", env); got != "git" {
		t.Errorf("none anywhere: %s", got)
	}
	bundled := filepath.Join(dir, "git", "cmd", "git.exe")
	touch(bundled)
	if got := findGit(missing, dir, "windows", env); got != bundled {
		t.Errorf("bundled: %s", got)
	}
	installed := filepath.Join(programs, "Git", "cmd", "git.exe")
	touch(installed)
	if got := findGit(missing, dir, "windows", env); got != installed {
		t.Errorf("installed, before the bundled one: %s", got)
	}
	if got := findGit(missing, dir, "darwin", env); got != "git" {
		t.Errorf("not on Windows: %s", got)
	}
}
