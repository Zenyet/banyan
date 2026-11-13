package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Zenyet/banyan/internal/git"
)

// Deleting a local change removes an untracked file, and stages a tracked
// file's deletion, with its changes gone.
func TestDeleteLocalFile(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	write("tracked.txt", "one\n")
	run("add", "tracked.txt")
	run("commit", "-q", "-m", "Base")
	write("tracked.txt", "one\ntwo\n")
	write("new.txt", "new\n")
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	status, err := repo.Status()
	if err != nil {
		t.Fatal(err)
	}
	var tracked, untracked *git.StatusEntry
	for i := range status {
		switch status[i].Path {
		case "tracked.txt":
			tracked = &status[i]
		case "new.txt":
			untracked = &status[i]
		}
	}
	if tracked == nil || untracked == nil {
		t.Fatalf("status = %+v", status)
	}
	if err := deleteLocalFile(repo, *tracked); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tracked.txt")); !os.IsNotExist(err) {
		t.Errorf("tracked file still there: %v", err)
	}
	if err := deleteLocalFile(repo, *untracked); err != nil {
		t.Fatal(err)
	}
	// The file is gone already; deleting it again is no error.
	if err := deleteLocalFile(repo, *untracked); err != nil {
		t.Fatal(err)
	}
	status, err = repo.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range status {
		if e.Path == "new.txt" {
			t.Errorf("new.txt is still in the status: %+v", e)
		}
		if e.Path == "tracked.txt" && e.DisplayStatus() != 'D' {
			t.Errorf("tracked.txt status = %c, want D", e.DisplayStatus())
		}
	}
}
