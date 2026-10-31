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

// Double-clicking a file in the Conflicts dialog opens the merge tool on
// it.
func TestDoubleClickConflictOpensMergeTool(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.CombinedOutput()
	}
	write := func(text string) {
		if err := os.WriteFile(filepath.Join(dir, "test.js"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	write("update\n")
	run("add", ".")
	run("commit", "-q", "-m", "init")
	run("checkout", "-q", "-b", "side")
	write("update\nside\n")
	run("commit", "-q", "-am", "side")
	run("checkout", "-q", "main")
	write("update\nmain\n")
	run("commit", "-q", "-am", "main")
	run("merge", "-q", "side")
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model.New(repo)
	defer m.Close()
	for deadline := time.Now().Add(5 * time.Second); len(m.Conflicts()) == 0 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if len(m.Conflicts()) == 0 {
		t.Fatal("no conflict")
	}
	s := &Shell{content: func(*ui.Context) {}}
	openConflicts(s, m)
	tt := ui.NewTester(s.view, 1400, 900)
	r, ok := tt.Find("test.js")
	if !ok {
		t.Fatalf("no file: %q", tt.Texts())
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	for _, d := range s.dialogs {
		if strings.HasPrefix(d.Title, "Merge Revisions for") {
			return
		}
	}
	t.Fatalf("no merge tool: %q", tt.Texts())
}
