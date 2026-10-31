package app

import (
	"os/exec"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/model"
)

// Collapsing a group of the branches tree, with rows after it, leaves
// the tree as it was until the list is built, rather than take rows out
// from under it.
func TestCollapseBranchGroup(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "first"},
		{"branch", "feature"},
		{"branch", "topic"},
		{"tag", "v1"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model.New(repo)
	defer m.Close()
	for deadline := time.Now().Add(5 * time.Second); len(m.Branches) < 4 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	ws := &Workspace{Shell: &Shell{}, Model: m}
	ws.log = newLogView(ws)
	ws.log.rebuildBranches()
	tt := ui.NewTester(func(c *ui.Context) { ws.log.branchesPanel(c) }, 300, 600)
	if !tt.HasText("topic") {
		t.Fatalf("no branches shown: %q", tt.Texts())
	}
	if err := tt.Click("Local"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("topic") || !tt.HasText("Tags") {
		t.Fatalf("Local did not collapse: %q", tt.Texts())
	}
}
