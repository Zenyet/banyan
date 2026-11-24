package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/model"
)

// Clicking a file in the Push dialog opens its diff over the dialog.
func TestPushFileOpensPreview(t *testing.T) {
	remote, dir := t.TempDir(), t.TempDir()
	// A git of the model's background work can still be writing the index
	// or the commit-graph as the test ends; removing the folder again once
	// that git has gone clears it.
	t.Cleanup(func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if os.RemoveAll(dir) == nil {
				return
			}
		}
		t.Errorf("could not remove %s", dir)
	})
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(remote, "init", "-q", "--bare")
	run(dir, "init", "-q", "-b", "main")
	run(dir, "config", "gc.auto", "0")
	run(dir, "config", "maintenance.auto", "false")
	write := func(text string) {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("one\n")
	run(dir, "add", ".")
	run(dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "first")
	run(dir, "remote", "add", "origin", remote)
	run(dir, "push", "-q", "origin", "main")
	write("two\n")
	if err := os.MkdirAll(filepath.Join(dir, "internal", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "app", "shell.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(dir, "add", ".")
	run(dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-qm", "second")

	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model.New(repo)
	t.Cleanup(m.Close)
	s := &Shell{content: func(*ui.Context) {}}
	ws := &Workspace{Shell: s, Model: m}
	openPush(ws)
	tt := ui.NewTester(s.view, 1000, 700)
	for deadline := time.Now().Add(5 * time.Second); !tt.HasText("a.txt") && time.Now().Before(deadline); {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	if !tt.HasText("a.txt") {
		t.Fatalf("no files in the push dialog: %q", tt.Texts())
	}
	if err := tt.Click("a.txt"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(s.dialogs) != 2 {
		t.Fatalf("clicking the file opened %d dialogs", len(s.dialogs))
	}
	if title := s.dialogs[1].Title; !strings.HasPrefix(title, "a.txt") {
		t.Fatalf("the preview is %q", title)
	}
	tt.Frame()
	if !tt.HasText("internal/app") {
		t.Fatalf("the preview has no file tree: %q", tt.Texts())
	}
}
