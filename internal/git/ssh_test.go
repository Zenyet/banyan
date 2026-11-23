package git

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeSSH runs the command ssh was asked to run on this machine, so that a
// repository over SSH can be tested without a server.
func fakeSSH(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	script := `#!/bin/sh
# ssh [-o option]... host command
while [ $# -gt 2 ]; do shift; done
exec sh -c "$2"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BANYAN_SSH", path)
	return path
}

func TestRemoteRepositoryRunsGitAndFilesOverSSH(t *testing.T) {
	local := newTempRepo(t)
	local.write("a.txt", "one\n")
	local.write("b.txt", "two\n")
	local.git("add", ".")
	local.git("commit", "-q", "-m", "Base")
	fakeSSH(t)

	repo, err := OpenSSH("dev@example.test", local.dir)
	if err != nil {
		t.Fatal(err)
	}
	if !repo.Remote() || repo.SSHHost() != "dev@example.test" {
		t.Fatalf("remote: %v %q", repo.Remote(), repo.SSHHost())
	}
	location := SSHLocation("dev@example.test", local.dir)
	if host, path, ok := ParseLocation(location); !ok || host != "dev@example.test" || path != local.dir {
		t.Fatalf("location %q -> %q %q %v", location, host, path, ok)
	}
	if repo.Location() != location {
		t.Fatalf("location %q, want %q", repo.Location(), location)
	}
	if repo.Label() != "dev@example.test:"+local.dir {
		t.Fatalf("label %q", repo.Label())
	}
	if repo.Name() != filepath.Base(local.dir) {
		t.Fatalf("name %q", repo.Name())
	}

	// Reading and writing files goes through the same connection.
	data, err := repo.ReadFile(repo.Path("a.txt"))
	if err != nil || string(data) != "one\n" {
		t.Fatalf("read: %q %v", data, err)
	}
	if err := repo.WriteFile(repo.Path("c.txt"), []byte("three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if text := local.read("c.txt"); text != "three\n" {
		t.Fatalf("written %q", text)
	}
	info, err := repo.Stat(repo.Path("c.txt"))
	if err != nil || info.Size != 6 || info.Mode.Perm() != 0o600 {
		t.Fatalf("stat: %+v %v", info, err)
	}
	if !repo.Exists(repo.Path("c.txt")) || !repo.IsDir(repo.root) {
		t.Fatal("exists or is dir")
	}
	if _, err := repo.ReadFile(repo.Path("missing.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}

	// Commands, including the ones a commit runs, are git on the host.
	status, err := repo.Status()
	if err != nil || len(status) != 1 || status[0].Path != "c.txt" {
		t.Fatalf("status: %+v %v", status, err)
	}
	if _, err := repo.Commit("Add c", []string{"c.txt"}, false, &CommitOptions{}); err != nil {
		t.Fatal(err)
	}
	head, ok := repo.Head()
	if !ok || len(head) != 40 {
		t.Fatal("no head")
	}
	log, err := repo.Log(&LogFilter{}, 10)
	if err != nil || len(log) != 2 || log[0].Subject != "Add c" {
		t.Fatalf("log: %+v %v", log, err)
	}

	// Shelves live in the remote .git, written through the same ssh
	// connection.
	local.write("a.txt", "changed\n")
	shelf, err := repo.Shelve("work", "Changes", []string{"a.txt"})
	if err != nil || shelf == nil {
		t.Fatalf("shelve: %v %v", shelf, err)
	}
	if shelves := repo.Shelves(); len(shelves) != 1 || shelves[0].Name != "work" {
		t.Fatalf("shelves: %+v", shelves)
	}
	if text := local.read("a.txt"); text != "one\n" {
		t.Fatalf("shelving left %q", text)
	}
	if _, _, err := repo.Unshelve(shelf.ID, nil); err != nil {
		t.Fatal(err)
	}
	if text := local.read("a.txt"); text != "changed\n" {
		t.Fatalf("unshelved %q", text)
	}
}

func TestParseLocationLeavesLocalPathsAlone(t *testing.T) {
	for _, local := range []string{"/home/me/repo", "C:\\work\\repo", "repo", ""} {
		if host, path, ok := ParseLocation(local); ok || host != "" || path != "" {
			t.Errorf("%q parsed as remote %q %q", local, host, path)
		}
	}
	if host, path, ok := ParseLocation("ssh://me@host/home/me/repo"); !ok || host != "me@host" || path != "/home/me/repo" {
		t.Errorf("remote parsed as %q %q %v", host, path, ok)
	}
}
