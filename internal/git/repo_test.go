package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Zenyet/banyan/internal/patch"
)

func TestDecorations(t *testing.T) {
	labels := parseDecorations("HEAD -> refs/heads/main, refs/remotes/origin/main, refs/remotes/origin/HEAD, tag: refs/tags/v1.0")
	want := []RefLabel{{"HEAD", RefHead}, {"main", RefLocal}, {"origin/main", RefRemote}, {"v1.0", RefTag}}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("got %v", labels)
	}
}

// tempRepo is a throwaway repository.
type tempRepo struct {
	t    *testing.T
	repo *Repo
	dir  string
}

func newTempRepo(t *testing.T) *tempRepo {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Test"}, {"config", "user.email", "t@example.com"}, {"config", "commit.gpgsign", "false"}} {
		if _, err := run(dir, args, false); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &tempRepo{t, repo, dir}
}

func (r *tempRepo) path(p string) string { return filepath.Join(r.dir, filepath.FromSlash(p)) }

func (r *tempRepo) write(path, text string) {
	r.t.Helper()
	p := r.path(path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *tempRepo) read(path string) string {
	r.t.Helper()
	text, err := os.ReadFile(r.path(path))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(text)
}

func (r *tempRepo) exists(path string) bool { return exists(r.path(path)) }

func (r *tempRepo) git(args ...string) string {
	r.t.Helper()
	out, err := r.repo.Git(args...)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (r *tempRepo) commit(path, text, message string) string {
	r.t.Helper()
	r.write(path, text)
	r.git("add", "-A")
	r.git("commit", "-q", "-m", message)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

func (r *tempRepo) subjects() []string { return lines(r.git("log", "--format=%s")) }

func (r *tempRepo) statusPaths() []string {
	r.t.Helper()
	status, err := r.repo.Status()
	if err != nil {
		r.t.Fatal(err)
	}
	var paths []string
	for _, e := range status {
		paths = append(paths, e.Path)
	}
	return paths
}

func (r *tempRepo) conflicted() bool {
	status, _ := r.repo.Status()
	return slices.ContainsFunc(status, func(e StatusEntry) bool { return e.IsConflicted() })
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func equal[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func ids(commits []Commit) []string {
	var out []string
	for _, c := range commits {
		out = append(out, c.ID)
	}
	return out
}

func subjectsOf(commits []Commit) []string {
	var out []string
	for _, c := range commits {
		out = append(out, c.Subject)
	}
	return out
}

func TestInteractiveRebaseSquashesRewordsAndDrops(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "0\n", "Base")
	one := r.commit("a.txt", "0\n1\n", "One")
	two := r.commit("b.txt", "2\n", "Two")
	three := r.commit("c.txt", "3\n", "Three")
	four := r.commit("d.txt", "4\n", "Four")
	equal(t, ids(must[[]Commit](t)(r.repo.RebaseCommits(one))), []string{one, two, three, four})
	must[string](t)(r.repo.InteractiveRebase(one, []RebaseStep{
		{one, RebaseAction{Kind: Pick}},
		{two, RebaseAction{Kind: Fixup}},
		{three, RebaseAction{Kind: Drop}},
		{four, RebaseAction{Kind: Reword, Message: "Four, reworded"}},
	}))
	equal(t, r.subjects(), []string{"Four, reworded", "One", "Base"})
	if !r.exists("b.txt") || r.exists("c.txt") || r.repo.State() != Clean {
		t.Fatal("files or state")
	}
}

func TestReorderingConflictingCommitsStopsAndAborts(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "0\n", "Base")
	one := r.commit("a.txt", "0\n1\n", "One")
	two := r.commit("a.txt", "0\n1\n2\n", "Two")
	if _, err := r.repo.InteractiveRebase(one, []RebaseStep{{two, RebaseAction{}}, {one, RebaseAction{}}}); err == nil {
		t.Fatal("expected a conflict")
	}
	if r.repo.State() != Rebasing || !r.conflicted() {
		t.Fatal("stopped on a conflict")
	}
	must[string](t)(r.repo.Abort(Rebasing))
	equal(t, r.repo.State(), Clean)
	equal(t, r.subjects(), []string{"Two", "One", "Base"})
}

// conflictingBranches has main and feature both change the one line of
// a.txt.
func conflictingBranches(t *testing.T) *tempRepo {
	r := newTempRepo(t)
	r.commit("a.txt", "start\nline\nend\n", "Base")
	r.git("checkout", "-q", "-b", "feature")
	r.commit("a.txt", "start\ntheirs\nend\n", "Theirs")
	r.git("checkout", "-q", "main")
	r.commit("a.txt", "start\nours\nend\n", "Ours")
	return r
}

func TestStoppedMergeIsDescribedAndMergedFromTheIndex(t *testing.T) {
	r := conflictingBranches(t)
	if _, err := r.repo.Merge("feature"); err == nil {
		t.Fatal("expected a conflict")
	}
	op := r.repo.Operation()
	equal(t, *op, Operation{State: Merging, Title: "Merging feature into main", Ours: "main", Theirs: "feature"})
	// Edits to the working file don't change what the merge tool starts
	// from.
	r.write("a.txt", "garbage\n")
	text := must[string](t)(r.repo.MergeConflictText("a.txt"))
	marker := func(c byte) string { return strings.Repeat(string(c), MarkerSize) }
	if !strings.Contains(text, marker('<')+" yours\nours\n"+marker('|')+" base\nline\n"+marker('=')+"\ntheirs\n"+marker('>')+" theirs\n") {
		t.Fatalf("text:\n%s", text)
	}
	theirs := false
	must[string](t)(r.repo.ResolveConflict("a.txt", &theirs))
	if r.conflicted() {
		t.Fatal("resolved")
	}
	must[string](t)(r.repo.ContinueOp(Merging))
	if r.repo.Operation() != nil {
		t.Fatal("finished")
	}
	equal(t, r.read("a.txt"), "start\ntheirs\nend\n")
}

func TestAcceptingTheSideThatDeletedAFileDeletesIt(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "one\n", "Base")
	r.git("checkout", "-q", "-b", "feature")
	r.git("rm", "-q", "a.txt")
	r.git("commit", "-q", "-m", "Delete")
	r.git("checkout", "-q", "main")
	r.commit("a.txt", "two\n", "Change")
	if _, err := r.repo.Merge("feature"); err == nil {
		t.Fatal("expected a conflict")
	}
	status := must[[]StatusEntry](t)(r.repo.Status())
	entry := status[slices.IndexFunc(status, func(e StatusEntry) bool { return e.IsConflicted() })]
	ours, theirs, ok := entry.ConflictSides()
	if !ok || ours != SideModified || theirs != SideDeleted || entry.CanMergeText() {
		t.Fatal("sides")
	}
	if _, err := r.repo.MergeConflictText("a.txt"); err == nil {
		t.Fatal("no text to merge")
	}
	theirsSide := false
	must[string](t)(r.repo.ResolveConflict("a.txt", &theirsSide))
	if r.exists("a.txt") || r.conflicted() {
		t.Fatal("deleted")
	}
}

func TestAbortingAStoppedMergeRestoresTheBranch(t *testing.T) {
	r := conflictingBranches(t)
	r.repo.Merge("feature")
	must[string](t)(r.repo.Abort(Merging))
	if r.repo.Operation() != nil {
		t.Fatal("aborted")
	}
	equal(t, r.read("a.txt"), "start\nours\nend\n")
}

func TestStoppedRebaseNamesTheBranchesAndProgress(t *testing.T) {
	r := conflictingBranches(t)
	if _, err := r.repo.Rebase("feature"); err == nil {
		t.Fatal("expected a conflict")
	}
	op := r.repo.Operation()
	equal(t, op.Title, "Rebasing main onto feature (commit 1 of 1)")
	equal(t, op.Ours, "feature")
	if !strings.HasSuffix(op.Theirs, " Ours") {
		t.Fatal(op.Theirs)
	}
}

// fourCommits makes Base, One, Two, Three on main, each adding a file.
func fourCommits(t *testing.T) (*tempRepo, []string) {
	r := newTempRepo(t)
	var out []string
	for _, s := range []string{"Base", "One", "Two", "Three"} {
		out = append(out, r.commit(s+".txt", s, s))
	}
	return r, out
}

func TestTagsPointAtTheirCommits(t *testing.T) {
	r, id := fourCommits(t)
	r.git("tag", "-a", "-m", "Annotated", "annotated", id[1])
	r.git("tag", "light", id[2])
	target := func(name string) string {
		for _, b := range must[[]Branch](t)(r.repo.Branches()) {
			if b.Name == name {
				return b.Target
			}
		}
		return ""
	}
	equal(t, target("annotated"), id[1])
	equal(t, target("light"), id[2])
}

func TestLogShowsCommitsOnlyATagHas(t *testing.T) {
	r, id := fourCommits(t)
	// A tagged commit on top of One that no branch has.
	r.git("checkout", "-q", "--detach", id[1])
	loose := r.commit("Loose.txt", "Loose", "Loose")
	r.git("tag", "-a", "-m", "Loose", "loose")
	r.git("tag", "on-branch", id[2])
	r.git("checkout", "-q", "-")
	equal(t, r.repo.tagsOffBranches(), []string{"refs/tags/loose"})
	log := must[[]Commit](t)(r.repo.Log(&LogFilter{}, 100))
	if !slices.Contains(ids(log), loose) || len(log) != 5 {
		t.Fatalf("log %v", subjectsOf(log))
	}
}

func TestRewordChangesOnlyTheMessage(t *testing.T) {
	r, id := fourCommits(t)
	// HEAD, with a staged change that must stay out of the commit.
	r.write("staged.txt", "x")
	r.git("add", "staged.txt")
	must[string](t)(r.repo.Rewrite(&Rewrite{Kind: RewriteReword, IDs: []string{id[3]}, Message: "Three, reworded"}))
	equal(t, r.subjects()[0], "Three, reworded")
	if !slices.Contains(r.statusPaths(), "staged.txt") {
		t.Fatal("staged change kept")
	}
	// An older commit, through a rebase.
	must[string](t)(r.repo.Rewrite(&Rewrite{Kind: RewriteReword, IDs: []string{id[1]}, Message: "One, reworded\n\nBody"}))
	equal(t, r.subjects(), []string{"Three, reworded", "Two", "One, reworded", "Base"})
	if !slices.Contains(r.statusPaths(), "staged.txt") {
		t.Fatal("staged change kept")
	}
}

func TestSquashMeldsCommitsIntoTheOldest(t *testing.T) {
	r, id := fourCommits(t)
	// Not next to each other: Three moves down onto One.
	must[string](t)(r.repo.Rewrite(&Rewrite{Kind: RewriteSquash, IDs: []string{id[3], id[1]}, Message: "One and Three"}))
	equal(t, r.subjects(), []string{"Two", "One and Three", "Base"})
	equal(t, lines(r.git("show", "--name-only", "--format=", "HEAD~1")), []string{"One.txt", "Three.txt"})
}

func TestFixupIntoParentKeepsTheParentMessage(t *testing.T) {
	r, id := fourCommits(t)
	must[string](t)(r.repo.Rewrite(&Rewrite{Kind: RewriteFixupIntoParent, IDs: []string{id[2]}}))
	equal(t, r.subjects(), []string{"Three", "One", "Base"})
	if !r.exists("Two.txt") {
		t.Fatal("Two.txt kept")
	}
	if _, err := r.repo.RewriteBase(&Rewrite{Kind: RewriteFixupIntoParent, IDs: []string{id[0]}}); err == nil {
		t.Fatal("the first commit has no parent")
	}
}

func TestDropRemovesCommitsAndKeepsLocalChanges(t *testing.T) {
	r, id := fourCommits(t)
	r.write("Base.txt", "edited")
	must[string](t)(r.repo.Rewrite(&Rewrite{Kind: RewriteDrop, IDs: []string{id[1], id[3]}}))
	equal(t, r.subjects(), []string{"Two", "Base"})
	if r.exists("One.txt") || r.read("Base.txt") != "edited" {
		t.Fatal("files")
	}
}

func TestRewritesStayOnTheCurrentBranch(t *testing.T) {
	r, id := fourCommits(t)
	r.git("checkout", "-q", "-b", "side", id[1])
	side := r.commit("side.txt", "side", "Side")
	r.git("checkout", "-q", "main")
	if _, err := r.repo.RewriteBase(&Rewrite{Kind: RewriteDrop, IDs: []string{side}}); err == nil {
		t.Fatal("side is not on main")
	}
	must[string](t)(r.repo.RewriteBase(&Rewrite{Kind: RewriteDrop, IDs: id}))
	if _, err := r.repo.Rewrite(&Rewrite{Kind: RewriteDrop, IDs: id}); err == nil {
		t.Fatal("not every commit")
	}
}

func TestUndoCommitKeepsTheChangesAndReturnsTheMessage(t *testing.T) {
	r, _ := fourCommits(t)
	equal(t, must[string](t)(r.repo.UndoCommit()), "Three")
	equal(t, r.subjects(), []string{"Two", "One", "Base"})
	status := must[[]StatusEntry](t)(r.repo.Status())
	if !slices.ContainsFunc(status, func(e StatusEntry) bool { return e.Path == "Three.txt" && e.Index == 'A' }) {
		t.Fatal("staged")
	}
}

func bareRemote(t *testing.T) *tempRepo {
	remote := newTempRepo(t)
	remote.git("config", "receive.denyCurrentBranch", "ignore")
	return remote
}

func TestPushedCommitsAreReported(t *testing.T) {
	r, id := fourCommits(t)
	remote := bareRemote(t)
	r.git("remote", "add", "origin", remote.dir)
	r.git("push", "-q", "origin", id[1]+":refs/heads/main")
	r.git("fetch", "-q", "origin")
	r.git("branch", "-q", "--set-upstream-to=origin/main")
	if up, ok := r.repo.PushedTo(id[1]); !ok || up != "origin/main" {
		t.Fatal("pushed")
	}
	if _, ok := r.repo.PushedTo(id[2]); ok {
		t.Fatal("not pushed")
	}
}

func TestComparesTwoRevisions(t *testing.T) {
	r, id := fourCommits(t)
	files := must[[]FileChange](t)(r.repo.ChangesBetween(id[1], id[3]))
	equal(t, []string{files[0].Path, files[1].Path}, []string{"Three.txt", "Two.txt"})
	if p := must[string](t)(r.repo.DiffBetween(id[1], id[3], &files[1])); !strings.Contains(p, "+Two") {
		t.Fatal(p)
	}
}

func TestShelvingTakesChangesOutAndUnshelvingBringsThemBack(t *testing.T) {
	r := newTempRepo(t)
	r.commit("keep.txt", "keep\n", "Base")
	r.commit("gone.txt", "gone\n", "Second")
	r.write("keep.txt", "keep\nmore\n")
	os.Remove(r.path("gone.txt"))
	r.write("new dir/new.txt", "new\n")
	r.write("other.txt", "not shelved\n")
	paths := []string{"gone.txt", "keep.txt", "new dir/new.txt"}

	shelf := must[*Shelf](t)(r.repo.Shelve("Work in progress", "Feature", paths))
	equal(t, shelf.Files, paths)
	equal(t, r.statusPaths(), []string{"other.txt"})
	if r.read("keep.txt") != "keep\n" || !r.exists("gone.txt") || r.exists("new dir/new.txt") {
		t.Fatal("work tree")
	}
	equal(t, r.repo.Shelves(), []Shelf{*shelf})
	if p := must[string](t)(r.repo.ShelfFilePatch(shelf.ID, "keep.txt")); !strings.Contains(p, "+more") {
		t.Fatal(p)
	}

	// One file first; the rest stays shelved.
	_, restored, err := r.repo.Unshelve(shelf.ID, []string{"new dir/new.txt"})
	check(t, err)
	equal(t, restored, []string{"new dir/new.txt"})
	equal(t, r.read("new dir/new.txt"), "new\n")
	status := must[[]StatusEntry](t)(r.repo.Status())
	entry := status[slices.IndexFunc(status, func(e StatusEntry) bool { return e.Path == "new dir/new.txt" })]
	if entry.IsUntracked() {
		t.Fatal("added, not untracked")
	}
	equal(t, r.repo.Shelves()[0].Files, []string{"gone.txt", "keep.txt"})

	back, _, err := r.repo.Unshelve(shelf.ID, nil)
	check(t, err)
	equal(t, back.Changelist, "Feature")
	if len(r.repo.Shelves()) != 0 || r.read("keep.txt") != "keep\nmore\n" || r.exists("gone.txt") {
		t.Fatal("everything back")
	}
}

func TestUnshelvingOntoChangedFilesMergesThreeWay(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "one\n", "Base")
	r.write("a.txt", "shelved\n")
	shelf := must[*Shelf](t)(r.repo.Shelve("Mine", "Changes", []string{"a.txt"}))
	r.commit("a.txt", "committed meanwhile\n", "Other")
	_, _, err := r.repo.Unshelve(shelf.ID, nil)
	check(t, err)
	text := r.read("a.txt")
	if !strings.Contains(text, "shelved") || !strings.Contains(text, "committed meanwhile") || !r.conflicted() {
		t.Fatal(text)
	}
}

func TestPatchesRoundTripAndFilesComeBackFromRevisions(t *testing.T) {
	r, id := fourCommits(t)
	p := filepath.Join(t.TempDir(), "x.patch")
	check(t, r.repo.CreatePatch(id[2:], p))
	text, _ := os.ReadFile(p)
	if !strings.Contains(string(text), "Subject: [PATCH] Two") || !strings.Contains(string(text), "Subject: [PATCH] Three") {
		t.Fatal(string(text))
	}
	must[string](t)(r.repo.Reset(id[1], ResetHard))
	must[string](t)(r.repo.ApplyPatch(p))
	if !r.exists("Two.txt") || !r.exists("Three.txt") {
		t.Fatal("applied")
	}
	r.write("One.txt", "changed")
	must[string](t)(r.repo.GetFromRevision(id[1], "One.txt"))
	equal(t, r.read("One.txt"), "One")
	must[string](t)(r.repo.GetFromRevision(id[0], "One.txt"))
	if r.exists("One.txt") {
		t.Fatal("removed")
	}
}

func TestOtherBranchesPushUpdateAndTrack(t *testing.T) {
	r := newTempRepo(t)
	remote := bareRemote(t)
	r.git("remote", "add", "origin", remote.dir)
	r.commit("a.txt", "a\n", "First")
	r.git("branch", "side")
	must[string](t)(r.repo.PushBranch("side"))
	if must[*PushTarget](t)(r.repo.PushTargetFor("side")).IsNew {
		t.Fatal("tracks after the push")
	}
	// Someone else moves the remote side branch on.
	other := newTempRepo(t)
	other.git("fetch", "-q", remote.dir, "side:side")
	other.git("checkout", "-q", "side")
	other.commit("b.txt", "b\n", "On side")
	other.git("push", "-q", remote.dir, "side:side")

	must[string](t)(r.repo.UpdateBranch("side"))
	equal(t, strings.TrimSpace(r.git("log", "-1", "--format=%s", "side")), "On side")
	if _, err := r.repo.UpdateBranch("main"); err == nil {
		t.Fatal("main is checked out")
	}
	must[string](t)(r.repo.SetUpstream("side", ""))
	if !must[*PushTarget](t)(r.repo.PushTargetFor("side")).IsNew {
		t.Fatal("untracked")
	}
	must[string](t)(r.repo.SetUpstream("side", "origin/side"))
	if must[*PushTarget](t)(r.repo.PushTargetFor("side")).IsNew {
		t.Fatal("tracked")
	}
}

func TestWorktreesAreListedAddedAndRemoved(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "First")
	r.git("branch", "existing")
	base, _ := filepath.EvalSymlinks(t.TempDir())
	newPath, existingPath := filepath.Join(base, "new"), filepath.Join(base, "existing")
	added := must[*Repo](t)(r.repo.AddWorktree(newPath, "fresh"))
	if b, _ := added.CurrentBranch(); b != "fresh" {
		t.Fatal(b)
	}
	must[*Repo](t)(r.repo.AddWorktree(existingPath, "existing"))

	trees := must[[]Worktree](t)(r.repo.Worktrees())
	if len(trees) != 3 || !trees[0].IsMain || !trees[0].IsCurrent || trees[0].Branch != "main" {
		t.Fatalf("%+v", trees)
	}
	fresh := trees[slices.IndexFunc(trees, func(w Worktree) bool { return w.Branch == "fresh" })]
	if fresh.IsCurrent || fresh.IsMain {
		t.Fatal("fresh")
	}
	// The other tree sees itself as current.
	seen := must[[]Worktree](t)(added.Worktrees())
	if !seen[slices.IndexFunc(seen, func(w Worktree) bool { return w.Branch == "fresh" })].IsCurrent {
		t.Fatal("current")
	}
	os.WriteFile(filepath.Join(newPath, "dirty.txt"), []byte("x"), 0o644)
	if !WorktreeIsDirty(newPath) {
		t.Fatal("dirty")
	}
	if _, err := r.repo.RemoveWorktree(newPath, false); err == nil {
		t.Fatal("dirty trees stay")
	}
	must[string](t)(r.repo.RemoveWorktree(newPath, true))
	os.RemoveAll(existingPath)
	if !slices.ContainsFunc(must[[]Worktree](t)(r.repo.Worktrees()), func(w Worktree) bool { return w.Prunable }) {
		t.Fatal("prunable")
	}
	must[string](t)(r.repo.PruneWorktrees())
	equal(t, len(must[[]Worktree](t)(r.repo.Worktrees())), 1)
}

func TestSubmodulesReportTheirStateAndUpdate(t *testing.T) {
	lib := newTempRepo(t)
	lib.commit("lib.txt", "v1\n", "Lib")
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "First")
	r.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", lib.dir, "lib")
	r.git("commit", "-q", "-m", "Add lib")
	subs := must[[]Submodule](t)(r.repo.Submodules())
	if subs[0].Path != "lib" || subs[0].State != SubmoduleUpToDate {
		t.Fatalf("%+v", subs)
	}
	// A fresh clone has it uninitialized until updated.
	cloneDir := filepath.Join(t.TempDir(), "clone")
	must[string](t)(run(r.dir, []string{"clone", "-q", r.dir, cloneDir}, false))
	clone := must[*Repo](t)(Open(cloneDir))
	equal(t, must[[]Submodule](t)(clone.Submodules())[0].State, SubmoduleUninitialized)
	// Recent git refuses local file submodules unless told otherwise.
	must[string](t)(clone.Git("-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive"))
	equal(t, must[[]Submodule](t)(clone.Submodules())[0].State, SubmoduleUpToDate)
	if !exists(filepath.Join(cloneDir, "lib", "lib.txt")) {
		t.Fatal("checked out")
	}
}

func TestCommitOptionsSetAuthorSignOffAndSkipHooks(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "First")
	r.write("a.txt", "b\n")
	must[string](t)(r.repo.Commit("Second", []string{"a.txt"}, false, &CommitOptions{Author: "Pair Programmer <pair@example.com>", SignOff: true}))
	log := r.git("log", "-1", "--format=%an <%ae>%n%B")
	if !strings.HasPrefix(log, "Pair Programmer <pair@example.com>") || !strings.Contains(log, "Signed-off-by: Test <t@example.com>") {
		t.Fatal(log)
	}
	// A failing pre-commit hook stops a commit unless hooks are skipped.
	hook := r.path(".git/hooks/pre-commit")
	os.MkdirAll(filepath.Dir(hook), 0o755)
	check(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	r.write("a.txt", "c\n")
	if _, err := r.repo.Commit("Third", []string{"a.txt"}, false, &CommitOptions{}); err == nil {
		t.Fatal("the hook stops it")
	}
	must[string](t)(r.repo.Commit("Third", []string{"a.txt"}, false, &CommitOptions{SkipHooks: true}))
	equal(t, r.subjects()[0], "Third")
	if !IsValidAuthor("Ada Park <ada@example.com>") || IsValidAuthor("ada@example.com") || IsValidAuthor(" <ada@example.com>") {
		t.Fatal("authors")
	}
}

func TestSignedCommitsShowTheirSignature(t *testing.T) {
	// SSH signing needs only ssh-keygen, unlike GPG.
	r := newTempRepo(t)
	key := r.path(".git/test-key")
	if exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "t@example.com", "-f", key).Run() != nil {
		t.Skip("no ssh-keygen")
	}
	public, _ := os.ReadFile(key + ".pub")
	allowed := r.path(".git/allowed-signers")
	os.WriteFile(allowed, []byte("t@example.com "+string(public)), 0o644)
	r.git("config", "gpg.format", "ssh")
	r.git("config", "gpg.ssh.allowedSignersFile", allowed)
	must[string](t)(r.repo.SetSigningKey(key + ".pub"))
	must[string](t)(r.repo.SetAlwaysSign(true))
	signing := r.repo.Signing()
	if !signing.Always || signing.Key == "" || signing.Format != "ssh" {
		t.Fatalf("%+v", signing)
	}
	r.write("a.txt", "a\n")
	must[string](t)(r.repo.Commit("Signed", []string{"a.txt"}, false, &CommitOptions{}))
	head, _ := r.repo.Head()
	_, sig, err := r.repo.CommitExtras(context.Background(), head)
	check(t, err)
	equal(t, sig.Kind, SignatureGood)
	r.write("a.txt", "b\n")
	no := false
	must[string](t)(r.repo.Commit("Unsigned", []string{"a.txt"}, false, &CommitOptions{Sign: &no}))
	head, _ = r.repo.Head()
	_, sig, err = r.repo.CommitExtras(context.Background(), head)
	check(t, err)
	equal(t, sig, Signature{})
}

func TestCommitGraphIsWrittenThenExtended(t *testing.T) {
	r, _ := fourCommits(t)
	info := r.path(".git/objects/info")
	check(t, r.repo.UpdateCommitGraph())
	if !exists(filepath.Join(info, "commit-graph")) {
		t.Fatal("written")
	}
	r.commit("Five.txt", "Five", "Five")
	check(t, r.repo.UpdateCommitGraph())
	if !exists(filepath.Join(info, "commit-graphs")) {
		t.Fatal("extended")
	}
	check(t, r.repo.RefreshIndex())
	equal(t, r.subjects()[0], "Five")
}

func TestInitMakesAFolderARepository(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	repo := must[*Repo](t)(Init(dir))
	equal(t, repo.Root(), dir)
	// Opening a folder inside it finds the repository.
	equal(t, must[*Repo](t)(Open(filepath.Join(dir, "sub"))).Root(), repo.Root())
}

func TestMergeMessagesNameTheMergedBranch(t *testing.T) {
	equal(t, mergedName("Merge branch 'feature'\n\n# Conflicts:"), "feature")
	equal(t, mergedName("Merge remote-tracking branch 'origin/x' into main"), "origin/x")
	equal(t, mergedName("Squashed commit"), "")
}

func TestFirstCommitCannotBeSquashed(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "0\n", "Base")
	one := r.commit("a.txt", "1\n", "One")
	if _, err := r.repo.InteractiveRebase(one, []RebaseStep{{one, RebaseAction{Kind: Squash}}}); err == nil {
		t.Fatal("refused")
	}
}

func TestPushSetsUpstreamThenReportsNothingOutgoing(t *testing.T) {
	r := newTempRepo(t)
	remote := bareRemote(t)
	r.git("remote", "add", "origin", remote.dir)
	r.commit("a.txt", "a\n", "First")
	r.commit("a.txt", "b\n", "Second")
	target := must[*PushTarget](t)(r.repo.PushTarget())
	if !target.IsNew || target.Remote != "origin" || target.RemoteBranch != "main" {
		t.Fatalf("%+v", target)
	}
	equal(t, len(must[[]Commit](t)(r.repo.Outgoing(target))), 2)
	must[string](t)(r.repo.PushTo(target, false, false))
	target = must[*PushTarget](t)(r.repo.PushTarget())
	if target.IsNew || len(must[[]Commit](t)(r.repo.Outgoing(target))) != 0 {
		t.Fatal("pushed")
	}
	must[string](t)(r.repo.CreateTag("v1", "HEAD", ""))
	must[string](t)(r.repo.PushTo(target, false, true))
	if _, err := run(remote.dir, []string{"rev-parse", "--verify", "refs/tags/v1"}, false); err != nil {
		t.Fatal("tag pushed")
	}
	r.commit("a.txt", "c\n", "Third")
	equal(t, subjectsOf(must[[]Commit](t)(r.repo.Outgoing(target))), []string{"Third"})
}

func TestHistoryFollowsRenamesAndBlameAttributesLines(t *testing.T) {
	r := newTempRepo(t)
	r.commit("old.txt", "one\n", "Create")
	r.git("mv", "old.txt", "new.txt")
	r.git("commit", "-q", "-m", "Rename")
	r.commit("new.txt", "one\ntwo\n", "Extend")
	history := must[[]FileRevision](t)(r.repo.FileHistory("new.txt"))
	var subjects []string
	for _, h := range history {
		subjects = append(subjects, h.Commit.Subject)
	}
	equal(t, subjects, []string{"Extend", "Rename", "Create"})
	equal(t, history[len(history)-1].Path, "old.txt")
	blame := must[[]BlameLine](t)(r.repo.Blame("HEAD", "new.txt"))
	if len(blame) != 2 || blame[0].Summary != "Create" || blame[1].Summary != "Extend" || blame[1].Text != "two" {
		t.Fatalf("%+v", blame)
	}
}

func TestCommitOnlySelectedPaths(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "Base")
	r.write("a.txt", "changed\n")
	r.write("new.txt", "new\n")
	r.write("skip.txt", "skip\n")
	must[string](t)(r.repo.Commit("Selected", []string{"a.txt", "new.txt"}, false, &CommitOptions{}))
	equal(t, r.statusPaths(), []string{"skip.txt"})
	details := must[*CommitDetails](t)(r.repo.CommitDetails("HEAD"))
	var files []string
	for _, f := range details.Files {
		files = append(files, string(f.Status)+" "+f.Path)
	}
	slices.Sort(files)
	equal(t, files, []string{"A new.txt", "M a.txt"})
}

func TestCompareListsCommitsOnEachSide(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "Base")
	r.git("checkout", "-q", "-b", "topic")
	r.commit("b.txt", "b\n", "Topic one")
	r.commit("c.txt", "c\n", "Topic two")
	r.git("checkout", "-q", "main")
	r.commit("d.txt", "d\n", "Main only")
	in, out, err := r.repo.CompareRefs("topic", "HEAD")
	check(t, err)
	equal(t, subjectsOf(in), []string{"Topic two", "Topic one"})
	equal(t, subjectsOf(out), []string{"Main only"})
}

func TestStashesExposeTheirCommitAndFiles(t *testing.T) {
	r := newTempRepo(t)
	base := r.commit("a.txt", "a\n", "Base")
	r.write("a.txt", "changed\n")
	must[string](t)(r.repo.Stash(&StashOptions{Message: "Work in progress", IncludeUntracked: true}))
	stashes := must[[]Stash](t)(r.repo.Stashes())
	if len(stashes) != 1 || stashes[0].Name != "stash@{0}" || !strings.Contains(stashes[0].Message, "Work in progress") || stashes[0].Commit.Parents[0] != base {
		t.Fatalf("%+v", stashes)
	}
	details := must[*CommitDetails](t)(r.repo.CommitDetails(stashes[0].Commit.ID))
	equal(t, details.Files, []FileChange{{Status: 'M', Path: "a.txt"}})
}

func TestNotIgnoredFiltersGitignoredPaths(t *testing.T) {
	r := newTempRepo(t)
	r.commit(".gitignore", "target/\n*.log\n", "Ignore")
	paths := []string{r.path("src/main.rs"), r.path("target/debug/app"), r.path("run.log")}
	equal(t, must[[]string](t)(r.repo.NotIgnored(paths)), []string{r.path("src/main.rs")})
}

func TestCloneDirNames(t *testing.T) {
	equal(t, CloneDirName("https://github.com/zed-industries/zed.git"), "zed")
	equal(t, CloneDirName("git@github.com:longbridge/gpui-kit.git"), "gpui-kit")
	equal(t, CloneDirName("/srv/repos/project/"), "project")
}

func TestCloneFromLocalRepository(t *testing.T) {
	source := newTempRepo(t)
	source.commit("a.txt", "a\n", "First")
	dest := filepath.Join(t.TempDir(), "clone")
	repo := must[*Repo](t)(Clone(source.dir, dest))
	if b, _ := repo.CurrentBranch(); b != "main" || !exists(filepath.Join(dest, "a.txt")) {
		t.Fatal("cloned")
	}
}

func TestManageRemotes(t *testing.T) {
	r := newTempRepo(t)
	if len(must[[]Remote](t)(r.repo.Remotes())) != 0 {
		t.Fatal("none yet")
	}
	must[string](t)(r.repo.AddRemote("origin", "https://example.com/a.git"))
	must[string](t)(r.repo.AddRemote("upstream", "git@example.com:b.git"))
	must[string](t)(r.repo.SetRemoteURL("origin", "https://example.com/c.git"))
	must[string](t)(r.repo.RenameRemote("upstream", "fork"))
	remotes := must[[]Remote](t)(r.repo.Remotes())
	equal(t, []string{remotes[0].Name, remotes[0].FetchURL, remotes[1].Name, remotes[1].FetchURL},
		[]string{"fork", "git@example.com:b.git", "origin", "https://example.com/c.git"})
	equal(t, remotes[1].PushURL, "https://example.com/c.git")
	must[string](t)(r.repo.RemoveRemote("fork"))
	equal(t, len(must[[]Remote](t)(r.repo.Remotes())), 1)
}

func excludedHunk(h *patch.Hunk) []patch.ChangedLine {
	var out []patch.ChangedLine
	for _, ix := range h.Changes() {
		out = append(out, patch.ChangedLine{Header: h.Header, Index: ix})
	}
	return out
}

func TestCommitPartialKeepsExcludedHunksInTheWorkTree(t *testing.T) {
	r := newTempRepo(t)
	var original strings.Builder
	for n := 1; n <= 20; n++ {
		fmt.Fprintf(&original, "line %d\n", n)
	}
	r.commit("f.txt", original.String(), "Base")
	r.commit("other.txt", "x\n", "Other")
	changed := strings.NewReplacer("line 2\n", "line two\n", "line 18\n", "line eighteen\n").Replace(original.String())
	r.write("f.txt", changed)
	r.write("other.txt", "y\n")
	hunks := patch.Parse(r.git("diff", "HEAD", "--", "f.txt")).Hunks
	equal(t, len(hunks), 2)
	// Commit other.txt whole and only the second hunk of f.txt.
	must[string](t)(r.repo.CommitPartial("Partial", []string{"other.txt"}, []PartialFile{{"f.txt", excludedHunk(&hunks[0])}}, false, &CommitOptions{}))
	committed := r.git("show", "HEAD:f.txt")
	if !strings.Contains(committed, "line 2\n") || !strings.Contains(committed, "line eighteen\n") {
		t.Fatal(committed)
	}
	equal(t, r.git("show", "HEAD:other.txt"), "y\n")
	equal(t, r.read("f.txt"), changed)
	equal(t, r.statusPaths(), []string{"f.txt"})
}

func TestCommitPartialCommitsSingleLines(t *testing.T) {
	r := newTempRepo(t)
	r.commit("f.txt", "a\nb\nc\n", "Base")
	changed := "a\nB\nc\nd\n"
	r.write("f.txt", changed)
	hunk := patch.Parse(r.git("diff", "HEAD", "--", "f.txt")).Hunks[0]
	// -b +B +d: leave out the removal of b and the addition of d.
	var excluded []patch.ChangedLine
	for _, ix := range hunk.Changes() {
		if hunk.Lines[ix] != "+B" {
			excluded = append(excluded, patch.ChangedLine{Header: hunk.Header, Index: ix})
		}
	}
	must[string](t)(r.repo.CommitPartial("Add B", nil, []PartialFile{{"f.txt", excluded}}, false, &CommitOptions{}))
	equal(t, r.git("show", "HEAD:f.txt"), "a\nb\nB\nc\n")
	equal(t, r.read("f.txt"), changed)
}

func TestCherryPicksAndRevertsSeveralCommits(t *testing.T) {
	r := newTempRepo(t)
	r.commit("base.txt", "base\n", "Base")
	r.git("checkout", "-q", "-b", "topic")
	one := r.commit("one.txt", "1\n", "One")
	two := r.commit("two.txt", "2\n", "Two")
	r.git("checkout", "-q", "main")
	must[string](t)(r.repo.CherryPick([]string{one, two}))
	equal(t, r.git("log", "--format=%s"), "Two\nOne\nBase\n")
	must[string](t)(r.repo.Revert(lines(r.git("rev-parse", "HEAD", "HEAD~1"))))
	if r.exists("one.txt") || r.exists("two.txt") {
		t.Fatal("reverted")
	}
}

func TestLogFiltersByPathAndDate(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "Add a")
	r.commit("dir/b.txt", "b\n", "Add b")
	r.commit("a.txt", "a2\n", "Change a")
	log := func(f LogFilter) []string { return subjectsOf(must[[]Commit](t)(r.repo.Log(&f, 10))) }
	equal(t, log(LogFilter{Paths: []string{"a.txt"}}), []string{"Change a", "Add a"})
	equal(t, log(LogFilter{Paths: []string{"dir"}}), []string{"Add b"})
	equal(t, log(LogFilter{Paths: []string{"dir", "a.txt"}}), []string{"Change a", "Add b", "Add a"})
	// An old commit on top must not hide the newer ones below it.
	cmd := exec.Command("git", "commit", "-q", "--allow-empty", "-m", "Old")
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2020-01-01T00:00:00")
	check(t, cmd.Run())
	equal(t, len(log(LogFilter{Since: "1.day.ago"})), 3)
	equal(t, len(log(LogFilter{Since: "2099-01-01"})), 0)
}

func TestBlamesWithPreviousRevisionsAcrossRenames(t *testing.T) {
	r := newTempRepo(t)
	first := r.commit("old.txt", "one\ntwo\n", "Add")
	r.git("mv", "old.txt", "new.txt")
	r.git("commit", "-qm", "Rename")
	second := r.commit("new.txt", "one\nTWO\nthree\n", "Change")
	ls := must[[]BlameLine](t)(r.repo.Blame("HEAD", "new.txt"))
	if len(ls) != 3 || ls[0].ID != first || ls[0].Path != "old.txt" || ls[0].OrigLine != 1 || ls[0].Previous != "" {
		t.Fatalf("%+v", ls[0])
	}
	if ls[1].ID != second || ls[1].OrigLine != 2 || ls[1].Summary != "Change" || ls[1].PreviousPath != "new.txt" {
		t.Fatalf("%+v", ls[1])
	}
	equal(t, must[[]BlameLine](t)(r.repo.Blame(ls[1].Previous, ls[1].PreviousPath))[1].Text, "two")
	equal(t, ls[2].Author, ls[1].Author)
}

func TestComparesAnyTwoBranches(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "Base")
	r.git("branch", "one")
	r.git("checkout", "-q", "-b", "two")
	r.commit("b.txt", "b\n", "Only in two")
	r.git("checkout", "-q", "one")
	r.commit("c.txt", "c\n", "Only in one")
	r.git("checkout", "-q", "-b", "elsewhere")
	in, out, err := r.repo.CompareRefs("two", "one")
	check(t, err)
	equal(t, subjectsOf(in), []string{"Only in two"})
	equal(t, subjectsOf(out), []string{"Only in one"})
	if _, _, err := r.repo.CompareRefs("two", "missing"); err == nil {
		t.Fatal("missing")
	}
}

func TestCreatesAnnotatedTags(t *testing.T) {
	r := newTempRepo(t)
	id := r.commit("a.txt", "a\n", "First")
	must[string](t)(r.repo.CreateTag("light", id, ""))
	must[string](t)(r.repo.CreateTag("v1.0", id, "Release 1.0"))
	equal(t, strings.TrimSpace(r.git("cat-file", "-t", "light")), "commit")
	equal(t, strings.TrimSpace(r.git("cat-file", "-t", "v1.0")), "tag")
	equal(t, strings.TrimSpace(r.git("tag", "-l", "--format=%(contents:subject)", "v1.0")), "Release 1.0")
	if _, err := r.repo.PushTag("v1.0"); err == nil {
		t.Fatal("no remote")
	}
}

func TestLocatesCommitsByAnyName(t *testing.T) {
	r := newTempRepo(t)
	first := r.commit("a.txt", "a\n", "First")
	r.commit("a.txt", "b\n", "Second")
	r.git("tag", "v1", first)
	id, above, err := r.repo.LocateCommit("v1")
	if err != nil || id != first || above != 1 {
		t.Fatal(id, above, err)
	}
	if id, _, _ := r.repo.LocateCommit(first[:7]); id != first {
		t.Fatal("short hash")
	}
	if _, above, _ := r.repo.LocateCommit("HEAD"); above != 0 {
		t.Fatal("HEAD")
	}
	for _, bad := range []string{"nope", "--all"} {
		if _, _, err := r.repo.LocateCommit(bad); err == nil {
			t.Fatal(bad)
		}
	}
}

func TestRemembersFavoriteAndRecentBranches(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "Base")
	if !slices.Contains(r.repo.FavoriteBranches(), "refs/heads/main") {
		t.Fatal("main by default")
	}
	check(t, r.repo.SetFavoriteBranch("refs/heads/main", false))
	check(t, r.repo.SetFavoriteBranch("refs/remotes/origin/dev", true))
	// The defaults that weren't unmarked stay.
	equal(t, r.repo.FavoriteBranches(), []string{"refs/heads/master", "refs/remotes/origin/dev"})
	first, _ := r.repo.CurrentBranch()
	for _, b := range []string{"one", "two"} {
		r.git("checkout", "-q", "-b", b)
	}
	r.git("checkout", "-q", "one")
	r.git("branch", "-q", "-D", "two")
	// Newest first, gone branches left out.
	equal(t, r.repo.RecentBranches(5), []string{"one", first})
}

func TestLinksToCommitsAndFilesOnTheWeb(t *testing.T) {
	base := func(url string) string {
		w, _ := ParseWebRemote(url)
		return w.Base
	}
	equal(t, base("git@github.com:Zenyet/banyan.git"), "https://github.com/Zenyet/banyan")
	equal(t, base("https://user@gitlab.com/group/sub/repo.git"), "https://gitlab.com/group/sub/repo")
	equal(t, base("ssh://git@git.example.com:2222/team/repo"), "https://git.example.com/team/repo")
	equal(t, base("http://localhost:3000/me/repo.git"), "http://localhost:3000/me/repo")
	equal(t, base("/srv/git/repo.git"), "")
	equal(t, base("../other"), "")

	github, _ := ParseWebRemote("git@github.com:o/r.git")
	equal(t, github.CommitURL("abc"), "https://github.com/o/r/commit/abc")
	equal(t, github.FileURL("abc", "src/a b.rs", 7), "https://github.com/o/r/blob/abc/src/a%20b.rs#L7")
	gitlab, _ := ParseWebRemote("git@gitlab.com:o/r.git")
	equal(t, gitlab.CommitURL("abc"), "https://gitlab.com/o/r/-/commit/abc")
	bitbucket, _ := ParseWebRemote("git@bitbucket.org:o/r.git")
	equal(t, bitbucket.FileURL("abc", "a.rs", 3), "https://bitbucket.org/o/r/src/abc/a.rs#lines-3")
	codeberg, _ := ParseWebRemote("https://codeberg.org/o/r")
	equal(t, codeberg.Name(), "codeberg.org")

	r := newTempRepo(t)
	if _, ok := r.repo.WebRemote(); ok {
		t.Fatal("no remote")
	}
	r.git("remote", "add", "upstream", "git@gitlab.com:x/y.git")
	r.git("remote", "add", "origin", "git@github.com:o/r.git")
	w, _ := r.repo.WebRemote()
	equal(t, w.Base, "https://github.com/o/r")
}

func TestMergesWithOptions(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "Base")
	r.git("checkout", "-q", "-b", "feature")
	r.commit("f.txt", "f\n", "Feature")
	r.git("checkout", "-q", "-")
	parents := func() int { return len(strings.Fields(r.git("log", "-1", "--format=%P"))) }
	// A fast-forward that's made a merge commit anyway, with our message.
	must[string](t)(r.repo.MergeWith("feature", &MergeOptions{FastForward: FastForwardNever, Message: "Bring in feature"}))
	equal(t, parents(), 2)
	equal(t, r.subjects()[0], "Bring in feature")

	r.git("reset", "-q", "--hard", "HEAD~1")
	r.commit("main.txt", "m\n", "Main")
	if _, err := r.repo.MergeWith("feature", &MergeOptions{FastForward: FastForwardOnly}); err == nil {
		t.Fatal("not a fast-forward")
	}
	// Squash leaves the changes to commit, without a merge in progress.
	must[string](t)(r.repo.MergeWith("feature", &MergeOptions{Squash: true}))
	if r.repo.State() != Clean || !slices.Contains(r.statusPaths(), "f.txt") {
		t.Fatal("squashed")
	}
	r.git("reset", "-q", "--hard")
	must[string](t)(r.repo.MergeWith("feature", &MergeOptions{NoCommit: true, FastForward: FastForwardNever}))
	equal(t, r.repo.State(), Merging)
}

func TestStashesSomeFilesAndUnstashesOntoABranch(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "Add a")
	r.commit("b.txt", "b\n", "Add b")
	r.write("a.txt", "a2\n")
	r.write("b.txt", "b2\n")
	r.write("new.txt", "n\n")
	must[string](t)(r.repo.Stash(&StashOptions{Message: "just a", Paths: []string{"a.txt"}}))
	if r.read("a.txt") != "a\n" || r.read("b.txt") != "b2\n" || !r.exists("new.txt") {
		t.Fatal("only a")
	}
	// Keep what's staged in the work tree.
	r.git("add", "b.txt")
	must[string](t)(r.repo.Stash(&StashOptions{KeepIndex: true, IncludeUntracked: true}))
	if r.read("b.txt") != "b2\n" || r.exists("new.txt") {
		t.Fatal("kept index")
	}
	r.git("reset", "-q", "--hard")
	stashes := must[[]Stash](t)(r.repo.Stashes())
	justA := stashes[slices.IndexFunc(stashes, func(s Stash) bool { return strings.Contains(s.Message, "just a") })]
	must[string](t)(r.repo.StashBranch(justA.Name, "from-stash"))
	if b, _ := r.repo.CurrentBranch(); b != "from-stash" || r.read("a.txt") != "a2\n" {
		t.Fatal("unstashed")
	}
	equal(t, len(must[[]Stash](t)(r.repo.Stashes())), 1)
}

func TestInteractiveRebaseStopsToEdit(t *testing.T) {
	r := newTempRepo(t)
	first := r.commit("a.txt", "a\n", "First")
	r.commit("b.txt", "b\n", "Second")
	r.commit("c.txt", "c\n", "Third")
	var plan []RebaseStep
	for _, c := range must[[]Commit](t)(r.repo.RebaseCommits(first)) {
		action := RebaseAction{Kind: Pick}
		if c.Subject == "Second" {
			action.Kind = Edit
		}
		plan = append(plan, RebaseStep{c.ID, action})
	}
	must[string](t)(r.repo.InteractiveRebase(first, plan))
	equal(t, r.repo.State(), Rebasing)
	if op := r.repo.Operation(); !strings.Contains(op.Title, "Second") {
		t.Fatal(op.Title)
	}
	// Amend the stopped commit, then carry on.
	r.write("b.txt", "b2\n")
	r.git("commit", "-qa", "--amend", "--no-edit")
	must[string](t)(r.repo.ContinueOp(Rebasing))
	equal(t, r.repo.State(), Clean)
	equal(t, r.subjects(), []string{"Third", "Second", "First"})
	equal(t, r.git("show", "HEAD~1:b.txt"), "b2\n")
}

func TestCommitExtrasListBranchesAndStopWhenCanceled(t *testing.T) {
	r, id := fourCommits(t)
	r.git("branch", "side", id[1])
	branches, _, err := r.repo.CommitExtras(context.Background(), id[1])
	check(t, err)
	equal(t, branches, []string{"main", "side"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.repo.CommitExtras(ctx, id[1]); err == nil {
		t.Fatal("canceled")
	}
}

func TestShowsAMergeAgainstEitherParent(t *testing.T) {
	r := newTempRepo(t)
	r.commit("base.txt", "base\n", "Base")
	r.git("checkout", "-q", "-b", "side")
	r.commit("side.txt", "side\n", "Side")
	r.git("checkout", "-q", "-")
	r.commit("main.txt", "main\n", "Main")
	r.git("merge", "-q", "--no-edit", "side")
	paths := func(base int) []string {
		var out []string
		for _, f := range must[*CommitDetails](t)(r.repo.CommitDetailsAgainst("HEAD", base)).Files {
			out = append(out, f.Path)
		}
		return out
	}
	equal(t, paths(0), []string{"side.txt"})
	equal(t, paths(1), []string{"main.txt"})
	// Out of range falls back to the last parent.
	equal(t, must[*CommitDetails](t)(r.repo.CommitDetailsAgainst("HEAD", 5)).Base, 1)
}

func TestAppliesAndRevertsPartOfACommit(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "Add a")
	r.write("b.txt", "b\n")
	r.commit("a.txt", "a\nmore\n", "Change a and add b")
	id := strings.TrimSpace(r.git("rev-parse", "HEAD"))
	parent := strings.TrimSpace(r.git("rev-parse", "HEAD~1"))
	// Undo only a.txt's part.
	must[string](t)(r.repo.ApplyCommitChanges(id, parent, []string{"a.txt"}, true))
	if r.read("a.txt") != "a\n" || !r.exists("b.txt") {
		t.Fatal("reverted a")
	}
	// And bring it back.
	must[string](t)(r.repo.ApplyCommitChanges(id, parent, []string{"a.txt"}, false))
	equal(t, r.read("a.txt"), "a\nmore\n")
	// A file changed since is left in conflict.
	r.commit("a.txt", "x\n", "Rewrite a")
	if _, err := r.repo.ApplyCommitChanges(id, parent, []string{"a.txt"}, true); err == nil {
		t.Fatal("conflict")
	}
	status := must[[]StatusEntry](t)(r.repo.Status())
	if !slices.ContainsFunc(status, func(e StatusEntry) bool { return e.Path == "a.txt" && e.IsConflicted() }) {
		t.Fatal("conflicted")
	}
	if _, err := r.repo.ApplyCommitChanges(id, parent, []string{"missing.txt"}, false); err == nil {
		t.Fatal("missing")
	}
}

func TestLogSearchesMessages(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "Add a")
	r.git("commit", "-q", "--allow-empty", "-m", "Fix [crash]", "-m", "Deep in the BODY.")
	search := func(text string) []string {
		return subjectsOf(must[[]Commit](t)(r.repo.Log(&LogFilter{Text: text}, 10)))
	}
	equal(t, search("[crash]"), []string{"Fix [crash]"})
	equal(t, search("deep in the body"), []string{"Fix [crash]"})
	equal(t, len(search("c.ash")), 0)
}

func TestLogFiltersByAuthors(t *testing.T) {
	r := newTempRepo(t)
	for _, c := range [][2]string{{"Ann", "one"}, {"Ann Lee", "two"}, {"B.o*b [x]", "three"}, {"Bob", "four"}} {
		r.git("commit", "-q", "--allow-empty", "-m", c[1], fmt.Sprintf("--author=%s <%s@example.com>", c[0], c[1]))
	}
	log := func(authors ...string) []string {
		return subjectsOf(must[[]Commit](t)(r.repo.Log(&LogFilter{Authors: authors}, 10)))
	}
	// Whole names only, special characters taken literally.
	equal(t, log("Ann"), []string{"one"})
	equal(t, log("B.o*b [x]"), []string{"three"})
	equal(t, log("Ann", "Bob"), []string{"four", "one"})
	equal(t, r.repo.UserName(), "Test")
}

func TestAddsUntrackedFilesToGitignore(t *testing.T) {
	r := newTempRepo(t)
	r.commit(".gitignore", "*.log", "Ignore logs")
	r.write("build/out.bin", "x")
	r.write("notes.txt", "x")
	check(t, r.repo.AddToGitignore("build/"))
	check(t, r.repo.AddToGitignore("notes.txt"))
	equal(t, r.read(".gitignore"), "*.log\n/build\n/notes.txt\n")
	equal(t, r.statusPaths(), []string{".gitignore"})
}

func TestSmartCheckoutCarriesLocalChanges(t *testing.T) {
	r := newTempRepo(t)
	var ls strings.Builder
	for n := 1; n <= 10; n++ {
		fmt.Fprintf(&ls, "%d\n", n)
	}
	r.commit("a.txt", ls.String(), "Base")
	r.git("checkout", "-q", "-b", "topic")
	r.commit("a.txt", strings.Replace(ls.String(), "1\n", "one\n", 1), "Topic edits line 1")
	r.git("checkout", "-q", "main")
	// A local edit of line 10 blocks a plain checkout.
	r.write("a.txt", strings.Replace(ls.String(), "10\n", "ten\n", 1))
	_, err := r.repo.CheckoutAny("topic", false)
	if !IsBlockedByLocalChanges(err) {
		t.Fatal(err)
	}
	equal(t, BlockedFiles(err.Error()), []string{"a.txt"})
	must[string](t)(r.repo.SmartCheckout("topic", false))
	if b, _ := r.repo.CurrentBranch(); b != "topic" {
		t.Fatal(b)
	}
	content := r.read("a.txt")
	if !strings.HasPrefix(content, "one\n") || !strings.HasSuffix(content, "ten\n") || len(must[[]Stash](t)(r.repo.Stashes())) != 0 {
		t.Fatal(content)
	}
}

func TestForceCheckoutDiscardsLocalChanges(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "base\n", "Base")
	r.git("checkout", "-q", "-b", "topic")
	r.commit("a.txt", "topic\n", "Topic")
	r.git("checkout", "-q", "main")
	r.write("a.txt", "local\n")
	must[string](t)(r.repo.ForceCheckout("topic", false))
	equal(t, r.read("a.txt"), "topic\n")
}

func TestUpdateProjectRebasesOntoIncomingAndKeepsLocalChanges(t *testing.T) {
	origin := bareRemote(t)
	origin.commit("a.txt", "a\n", "Base")
	local := newTempRepo(t)
	local.git("remote", "add", "origin", origin.dir)
	local.git("fetch", "-q", "origin")
	local.git("reset", "-q", "--hard", "origin/main")
	local.git("branch", "-q", "--set-upstream-to=origin/main")
	origin.commit("b.txt", "b\n", "Incoming")
	local.commit("c.txt", "c\n", "Local commit")
	local.write("a.txt", "uncommitted\n")
	must[string](t)(local.repo.UpdateProject(UpdateRebase))
	equal(t, local.subjects(), []string{"Local commit", "Incoming", "Base"})
	if !local.exists("b.txt") || local.read("a.txt") != "uncommitted\n" {
		t.Fatal("updated")
	}
}

func TestUpdateProjectNeedsAnUpstream(t *testing.T) {
	r := newTempRepo(t)
	r.commit("a.txt", "a\n", "Base")
	_, err := r.repo.UpdateProject(UpdateMerge)
	if err == nil || !strings.Contains(err.Error(), "no tracked remote branch") {
		t.Fatal(err)
	}
}

func TestDiffWithWorktreeSinceARevision(t *testing.T) {
	r := newTempRepo(t)
	base := r.commit("a.txt", "a\n", "Base")
	r.commit("b.txt", "b\n", "Add b")
	r.write("a.txt", "changed\n")
	files := must[[]FileChange](t)(r.repo.ChangesBetween(base, ""))
	equal(t, files, []FileChange{{Status: 'M', Path: "a.txt"}, {Status: 'A', Path: "b.txt"}})
	if p := must[string](t)(r.repo.DiffBetween(base, "", &files[0])); !strings.Contains(p, "+changed") {
		t.Fatal(p)
	}
}
