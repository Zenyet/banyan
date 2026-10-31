package app

import (
	"os/exec"
	"slices"
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
	ws := branchesWorkspace(t)
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

// The groups and folders of the branches tree have menus of their own,
// and Collapse All closes every folder inside.
func TestBranchGroupMenus(t *testing.T) {
	ws := branchesWorkspace(t)
	tt := ui.NewTester(func(c *ui.Context) { ws.log.branchesPanel(c) }, 300, 600)
	for _, tc := range []struct {
		row  string
		want []string
	}{
		{"Local", []string{"New Branch...", "-", "Expand All", "Collapse All"}},
		{"Remote", []string{"Fetch", "Manage Remotes…", "-", "Expand All", "Collapse All"}},
		{"origin", []string{"Fetch 'origin'", "Manage Remotes…", "-", "Expand All", "Collapse All"}},
		{"feature", []string{"Expand All", "Collapse All"}},
		{"Tags", []string{"New Tag...", "Push All Tags", "-", "Expand All", "Collapse All"}},
	} {
		if err := tt.RightClick(tc.row); err != nil {
			t.Fatal(err)
		}
		if got := tt.Menu(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: the menu shows %q, want %q", tc.row, got, tc.want)
		}
		tt.CloseMenu()
	}
	if err := tt.RightClick("Local"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Collapse All"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("topic") || !tt.HasText("Remote") {
		t.Fatalf("Local did not collapse: %q", tt.Texts())
	}
	if err := tt.RightClick("Local"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Expand All"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("one") {
		t.Fatalf("Local's folders did not open: %q", tt.Texts())
	}
}

// branchesWorkspace is a workspace on a repository with branches main,
// topic and feature/one, a tag, and a remote origin with its main.
func branchesWorkspace(t *testing.T) *Workspace {
	t.Helper()
	remote, dir := t.TempDir(), t.TempDir()
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(remote, "init", "-q", "--bare")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "first"},
		{"branch", "topic"},
		{"branch", "feature/one"},
		{"tag", "v1"},
		{"remote", "add", "origin", remote},
		{"push", "-q", "origin", "main"},
	} {
		run(dir, args...)
	}
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model.New(repo)
	t.Cleanup(m.Close)
	for deadline := time.Now().Add(5 * time.Second); len(m.Branches) < 5 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	ws := &Workspace{Shell: &Shell{}, Model: m}
	ws.log = newLogView(ws)
	ws.log.rebuildBranches()
	return ws
}
