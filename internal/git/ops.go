package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/patch"
)

// IsBlockedByLocalChanges reports whether a failed checkout was refused
// because of local changes.
func IsBlockedByLocalChanges(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "would be overwritten by checkout") || strings.Contains(m, "would be removed by checkout")
}

// BlockedFiles returns the files git lists in a refused checkout's message.
func BlockedFiles(message string) []string {
	ls := lines(message)
	start := slices.IndexFunc(ls, func(l string) bool { return strings.HasSuffix(strings.TrimRight(l, " \t"), "by checkout:") })
	if start < 0 {
		return nil
	}
	var files []string
	for _, l := range ls[start+1:] {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "Please") || strings.HasPrefix(l, "Aborting") {
			break
		}
		files = append(files, l)
	}
	return files
}

// mergedName is the branch or commit a merge message names: feature in
// "Merge branch 'feature' into main" or "Merge remote-tracking branch
// 'origin/x'".
func mergedName(message string) string {
	first, _, _ := strings.Cut(message, "\n")
	for _, prefix := range []string{"Merge branch '", "Merge remote-tracking branch '", "Merge tag '", "Merge commit '"} {
		if rest, ok := strings.CutPrefix(first, prefix); ok {
			name, _, _ := strings.Cut(rest, "'")
			return name
		}
	}
	return ""
}

// Commit commits exactly paths with their working tree content, like a
// JetBrains changelist.
func (r *Repo) Commit(message string, paths []string, amend bool, options *CommitOptions) (string, error) {
	if len(paths) > 0 {
		if _, err := r.Git(append([]string{"add", "-A", "--"}, paths...)...); err != nil {
			return "", err
		}
	}
	args := []string{"commit", "-m", message}
	if amend {
		args = append(args, "--amend")
	}
	args = append(args, options.args()...)
	if len(paths) > 0 && r.HasHead() && r.State() == Clean {
		args = append(append(args, "--only", "--"), paths...)
	}
	return r.Git(args...)
}

// PartialFile pairs a modified file with the changed lines to leave out of
// a commit.
type PartialFile struct {
	Path     string
	Excluded []patch.ChangedLine
}

// CommitPartial commits whole files plus only some changed lines of others.
//
// The index is rebuilt from HEAD: staging done outside the app is not kept,
// matching the changelist model of the Commit tab.
func (r *Repo) CommitPartial(message string, full []string, partial []PartialFile, amend bool, options *CommitOptions) (string, error) {
	if r.State() != Clean {
		return "", errors.New("partial commits are not possible while a merge or rebase is in progress")
	}
	if _, err := r.Git("reset", "-q"); err != nil {
		return "", err
	}
	if len(full) > 0 {
		if _, err := r.Git(append([]string{"add", "-A", "--"}, full...)...); err != nil {
			return "", err
		}
	}
	for _, p := range partial {
		diff, err := r.Git("diff", "HEAD", "--", p.Path)
		if err != nil {
			return "", err
		}
		keep := func(h *patch.Hunk, ix int) bool {
			return !slices.Contains(p.Excluded, patch.ChangedLine{Header: h.Header, Index: ix})
		}
		if selected, ok := patch.Select(patch.Parse(diff), keep); ok {
			if _, err := r.GitWithInput(selected, "apply", "--cached", "--recount", "-"); err != nil {
				return "", err
			}
		}
	}
	args := []string{"commit", "-m", message}
	if amend {
		args = append(args, "--amend")
	}
	return r.Git(append(args, options.args()...)...)
}

// Signing is the repository's signing settings, local or global.
func (r *Repo) Signing() SigningConfig {
	get := func(key string) string {
		v, _ := r.trimmed("config", "--get", key)
		return v
	}
	format := get("gpg.format")
	if format == "" {
		format = "openpgp"
	}
	return SigningConfig{Always: get("commit.gpgsign") == "true", Key: get("user.signingkey"), Format: format}
}

// SetAlwaysSign signs every commit in this repository, or not.
func (r *Repo) SetAlwaysSign(always bool) (string, error) {
	return r.Git("config", "--local", "commit.gpgsign", strconv.FormatBool(always))
}

// SetSigningKey sets the key this repository signs with; empty uses the
// default key.
func (r *Repo) SetSigningKey(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		r.Git("config", "--local", "--unset", "user.signingkey")
		return "", nil
	}
	return r.Git("config", "--local", "user.signingkey", strings.TrimSpace(key))
}

// AddToGitignore appends path to the top-level .gitignore, anchored to the
// root.
func (r *Repo) AddToGitignore(path string) error {
	file := filepath.Join(r.root, ".gitignore")
	text, _ := os.ReadFile(file)
	if len(text) > 0 && text[len(text)-1] != '\n' {
		text = append(text, '\n')
	}
	text = append(text, "/"+strings.TrimRight(path, "/")+"\n"...)
	if err := os.WriteFile(file, text, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", file, err)
	}
	return nil
}

func (r *Repo) Rollback(entries []StatusEntry) error {
	for _, e := range entries {
		if e.IsUntracked() {
			continue
		}
		var err error
		if e.Index == 'A' {
			_, err = r.Git("rm", "-q", "--cached", "-f", "--", e.Path)
		} else {
			args := []string{"restore", "--source=HEAD", "--staged", "--worktree", "--", e.Path}
			if e.OrigPath != "" {
				args = append(args, e.OrigPath)
			}
			_, err = r.Git(args...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) Checkout(name string) (string, error) { return r.Git("checkout", name) }

// localName is the local branch a remote branch is checked out as.
func localName(remoteBranch string) string {
	if _, name, ok := strings.Cut(remoteBranch, "/"); ok {
		return name
	}
	return remoteBranch
}

// CheckoutRemote checks out a remote branch, creating a local tracking
// branch for it.
func (r *Repo) CheckoutRemote(remoteBranch string) (string, error) {
	local := localName(remoteBranch)
	if r.ok("rev-parse", "--verify", "-q", "refs/heads/"+local) {
		return r.Git("checkout", local)
	}
	return r.Git("checkout", "-b", local, "--track", remoteBranch)
}

// CheckoutAny checks out a local branch, a remote branch (tracking it
// locally) or a revision.
func (r *Repo) CheckoutAny(target string, remote bool) (string, error) {
	if remote {
		return r.CheckoutRemote(target)
	}
	return r.Checkout(target)
}

// SmartCheckout stashes local changes, checks out, and brings the changes
// back, like JetBrains' "Smart Checkout".
func (r *Repo) SmartCheckout(target string, remote bool) (string, error) {
	if _, err := r.Git("stash", "push", "--include-untracked", "-m", "Banyan: smart checkout of "+target); err != nil {
		return "", err
	}
	if _, err := r.CheckoutAny(target, remote); err != nil {
		// Put things back the way they were.
		r.Git("stash", "pop", "--index")
		return "", err
	}
	out, err := r.Git("stash", "pop")
	if err != nil {
		return "", fmt.Errorf("Checked out %s, but restoring your changes conflicted. Resolve the conflicts "+
			"in the Commit tab; your changes are also kept in the top stash.\n%w", target, err)
	}
	return out, nil
}

// ForceCheckout checks out and discards local changes that would be
// overwritten.
func (r *Repo) ForceCheckout(target string, remote bool) (string, error) {
	if remote {
		local := localName(target)
		if r.ok("rev-parse", "--verify", "-q", "refs/heads/"+local) {
			return r.Git("checkout", "-f", local)
		}
		return r.Git("checkout", "-f", "-b", local, "--track", target)
	}
	return r.Git("checkout", "-f", target)
}

func (r *Repo) CreateBranch(name, start string, checkout bool) (string, error) {
	if checkout {
		return r.Git("checkout", "-b", name, start)
	}
	return r.Git("branch", name, start)
}

func (r *Repo) RenameBranch(old, new string) (string, error) { return r.Git("branch", "-m", old, new) }

func (r *Repo) DeleteBranch(name string, force bool) (string, error) {
	flag := "-d"
	if force {
		flag = "-D"
	}
	return r.Git("branch", flag, name)
}

func (r *Repo) DeleteRemoteBranch(remoteBranch string) (string, error) {
	remote, name, ok := strings.Cut(remoteBranch, "/")
	if !ok {
		return "", fmt.Errorf("not a remote branch: %s", remoteBranch)
	}
	return r.Git("push", remote, "--delete", name)
}

// CreateTag creates a lightweight tag, or an annotated one with a message.
func (r *Repo) CreateTag(name, target, message string) (string, error) {
	if message != "" {
		return r.Git("tag", "-a", "-m", message, "--end-of-options", name, target)
	}
	return r.Git("tag", "--end-of-options", name, target)
}

// PushTag pushes one tag to the remote the current branch tracks, else
// origin.
func (r *Repo) PushTag(name string) (string, error) {
	remote, ok := r.DefaultRemote()
	if !ok {
		return "", errors.New("the repository has no remote")
	}
	return r.Git("push", remote, "refs/tags/"+name)
}

func (r *Repo) DeleteTag(name string) (string, error) { return r.Git("tag", "-d", name) }

func (r *Repo) Merge(name string) (string, error) { return r.MergeWith(name, &MergeOptions{}) }

func (r *Repo) MergeWith(name string, options *MergeOptions) (string, error) {
	return r.Git(append(append([]string{"merge"}, options.Args()...), name)...)
}

func (r *Repo) Rebase(onto string) (string, error) { return r.Git("rebase", onto) }

// CherryPick applies ids in order, oldest first.
func (r *Repo) CherryPick(ids []string) (string, error) {
	return r.Git(append([]string{"cherry-pick"}, ids...)...)
}

// Revert reverts ids in order, newest first.
func (r *Repo) Revert(ids []string) (string, error) {
	return r.Git(append([]string{"revert", "--no-edit"}, ids...)...)
}

func (r *Repo) Reset(id string, mode ResetMode) (string, error) {
	return r.Git("reset", [...]string{"--soft", "--mixed", "--hard"}[mode], id)
}

func (r *Repo) Abort(state RepoState) (string, error) {
	out, err := r.abortOp(state)
	r.cleanupRebaseFiles()
	return out, err
}

// cleanupRebaseFiles removes the reworded messages of an interactive
// rebase that is no longer running.
func (r *Repo) cleanupRebaseFiles() {
	if r.State() == Clean {
		os.RemoveAll(r.rebaseFilesDir())
	}
}

// rebaseFilesDir is kept inside the repository's git directory so
// concurrent rebases of different repositories never share it.
func (r *Repo) rebaseFilesDir() string { return filepath.Join(r.gitDir, "banyan-rebase") }

func (r *Repo) abortOp(state RepoState) (string, error) {
	switch state {
	case Merging:
		return r.Git("merge", "--abort")
	case Rebasing:
		return r.Git("rebase", "--abort")
	case CherryPicking:
		return r.Git("cherry-pick", "--abort")
	case Reverting:
		return r.Git("revert", "--abort")
	}
	return "", nil
}

func (r *Repo) ContinueOp(state RepoState) (string, error) {
	var args []string
	switch state {
	case Merging:
		args = []string{"-c", "core.editor=true", "commit", "--no-edit"}
	case Rebasing:
		args = []string{"-c", "core.editor=true", "rebase", "--continue"}
	case CherryPicking:
		args = []string{"-c", "core.editor=true", "cherry-pick", "--continue"}
	case Reverting:
		args = []string{"-c", "core.editor=true", "revert", "--continue"}
	default:
		return "", nil
	}
	out, err := r.Git(args...)
	r.cleanupRebaseFiles()
	return out, err
}

// ResolveConflict takes one side of a conflicted file (ours true or false),
// or with nil marks it resolved as it is. Taking the side that deleted the
// file deletes it.
func (r *Repo) ResolveConflict(path string, ours *bool) (string, error) {
	if ours != nil {
		stages, err := r.conflictStages(path)
		if err != nil {
			return "", err
		}
		stage, flag := 3, "--theirs"
		if *ours {
			stage, flag = 2, "--ours"
		}
		if !slices.Contains(stages, stage) {
			return r.Git("rm", "--quiet", "--", path)
		}
		if _, err := r.Git("checkout", flag, "--", path); err != nil {
			return "", err
		}
	}
	return r.Git("add", "-A", "--", path)
}

// conflictStages is which index stages a conflicted file has: 1 base, 2
// ours, 3 theirs.
func (r *Repo) conflictStages(path string) ([]int, error) {
	out, err := r.Git("ls-files", "-u", "--", path)
	if err != nil {
		return nil, err
	}
	var stages []int
	for _, line := range lines(out) {
		meta, _, _ := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if len(f) > 0 {
			if n, err := strconv.Atoi(f[len(f)-1]); err == nil {
				stages = append(stages, n)
			}
		}
	}
	return stages, nil
}

// MergeConflictText is git's three-way merge of a conflicted file's yours,
// base and theirs versions, with diff3-style markers of MarkerSize around
// what still conflicts. Changes that merged cleanly are already applied.
func (r *Repo) MergeConflictText(path string) (string, error) {
	// git knows the empty blob in every repository.
	const emptyBlob = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
	stages, err := r.conflictStages(path)
	if err != nil {
		return "", err
	}
	if !slices.Contains(stages, 2) || !slices.Contains(stages, 3) {
		return "", fmt.Errorf("%s was deleted on one side; choose Accept Yours or Accept Theirs", path)
	}
	base := emptyBlob
	if slices.Contains(stages, 1) {
		base = ":1:" + path
	}
	cmd := command(r.root, []string{
		"merge-file", "-p", "--object-id", "--zdiff3", "--marker-size", strconv.Itoa(MarkerSize),
		"-L", "yours", "-L", "base", "-L", "theirs", ":2:" + path, base, ":3:" + path,
	})
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	// The exit code is the number of conflicts; errors are negative.
	if err != nil {
		code := cmd.ProcessState.ExitCode()
		if code < 0 || code >= 128 {
			return "", fmt.Errorf("Cannot merge %s: %s", path, strings.TrimSpace(stderr.String()))
		}
	}
	text := string(out)
	if strings.ContainsRune(text, 0) {
		return "", fmt.Errorf("%s is a binary file; choose Accept Yours or Accept Theirs", path)
	}
	return text, nil
}

// Operation is the stopped merge, rebase, cherry-pick or revert, if any.
func (r *Repo) Operation() *Operation {
	state := r.State()
	if state == Clean {
		return nil
	}
	read := func(name string) string {
		text, err := os.ReadFile(filepath.Join(r.gitDir, name))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(text))
	}
	head, ok := r.CurrentBranch()
	if !ok {
		if id, ok := r.Head(); ok {
			head = Short(id)
		} else {
			head = "HEAD"
		}
	}
	op := func(title, ours, theirs string) *Operation {
		return &Operation{State: state, Title: title, Ours: ours, Theirs: theirs}
	}
	switch state {
	case Merging:
		theirs := mergedName(read("MERGE_MSG"))
		if theirs == "" {
			if id := read("MERGE_HEAD"); id != "" {
				theirs = r.describe(id)
			} else {
				theirs = i18n.T("the other branch")
			}
		}
		return op(i18n.Tf("Merging {} into {}", theirs, head), head, theirs)
	case Rebasing:
		dir := "rebase-apply"
		if exists(filepath.Join(r.gitDir, "rebase-merge")) {
			dir = "rebase-merge"
		}
		branch := strings.TrimPrefix(read(dir+"/head-name"), "refs/heads/")
		if branch == "" {
			branch = i18n.T("detached HEAD")
		}
		onto := i18n.T("upstream")
		if id := read(dir + "/onto"); id != "" {
			onto = r.describe(id)
		}
		step, total := "next", "last"
		if dir == "rebase-merge" {
			step, total = "msgnum", "end"
		}
		progress := ""
		if s, t := read(dir+"/"+step), read(dir+"/"+total); s != "" && t != "" {
			progress = i18n.Tf(" (commit {} of {})", s, t)
		}
		applying := branch
		if id := read("REBASE_HEAD"); id != "" {
			applying = r.commitLabel(id)
		}
		// An "edit" step: git stopped after picking the commit, for it to
		// be amended.
		if dir == "rebase-merge" {
			if id := read("rebase-merge/amend"); id != "" {
				commit := r.commitLabel(id)
				return op(i18n.Tf("Stopped to edit {} while rebasing {}: amend it from the Commit tab, then continue", commit, branch), onto, applying)
			}
		}
		return op(i18n.Tf("Rebasing {} onto {}{}", branch, onto, progress), onto, applying)
	case CherryPicking:
		commit := ""
		if id := read("CHERRY_PICK_HEAD"); id != "" {
			commit = r.commitLabel(id)
		}
		return op(i18n.Tf("Cherry-picking {}", commit), head, commit)
	case Reverting:
		commit := ""
		if id := read("REVERT_HEAD"); id != "" {
			commit = r.commitLabel(id)
		}
		return op(i18n.Tf("Reverting {}", commit), head, i18n.Tf("revert of {}", commit))
	}
	return nil
}

// describe is a branch-relative name for a commit, like feature or main~2.
func (r *Repo) describe(id string) string {
	if name, ok := r.trimmed("name-rev", "--name-only", "--no-undefined", "--exclude=refs/tags/*", id); ok {
		return strings.TrimPrefix(name, "remotes/")
	}
	return Short(id)
}

// commitLabel is "abc1234 Subject" of a commit.
func (r *Repo) commitLabel(id string) string {
	if label, ok := r.trimmed("log", "-1", "--format=%h %s", id); ok {
		return label
	}
	return Short(id)
}

func (r *Repo) Fetch() (string, error) { return r.Git("fetch", "--all", "--prune") }

// FetchQuietly fetches in the background: it never asks for credentials, as
// a prompt nobody asked for would be confusing. Stored credentials still
// work.
func (r *Repo) FetchQuietly() error {
	cmd := command(r.root, []string{"fetch", "--all", "--prune", "--quiet"})
	env := cmd.Env[:0:0]
	for _, kv := range cmd.Env {
		if !strings.HasPrefix(kv, "GIT_ASKPASS=") && !strings.HasPrefix(kv, "SSH_ASKPASS=") && !strings.HasPrefix(kv, "SSH_ASKPASS_REQUIRE=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// UpdateProject is JetBrains' "Update Project": pull the tracked branch by
// merging or rebasing, stashing local changes around it.
func (r *Repo) UpdateProject(method UpdateMethod) (string, error) {
	branch, ok := r.CurrentBranch()
	if !ok {
		return "", errors.New("cannot update a detached HEAD")
	}
	if !r.ok("config", "--get", "branch."+branch+".merge") {
		return "", fmt.Errorf("%s has no tracked remote branch; push it first or set an upstream", branch)
	}
	args := []string{"pull", "--autostash", "--no-edit"}
	switch method {
	case UpdateMerge:
		args = append(args, "--no-rebase")
	case UpdateRebase:
		args = append(args, "--rebase")
	case UpdateFastForwardOnly:
		args = append(args, "--no-rebase", "--ff-only")
	}
	return r.Git(args...)
}

// PushTarget is where the current branch pushes to: its upstream, or a new
// branch of the same name on the first remote.
func (r *Repo) PushTarget() (*PushTarget, error) {
	branch, ok := r.CurrentBranch()
	if !ok {
		return nil, errors.New("cannot push a detached HEAD")
	}
	return r.PushTargetFor(branch)
}

// PushTargetFor is where local branch branch pushes to, as PushTarget says.
func (r *Repo) PushTargetFor(branch string) (*PushTarget, error) {
	remote, okRemote := r.trimmed("config", "--get", "branch."+branch+".remote")
	merge, okMerge := r.trimmed("config", "--get", "branch."+branch+".merge")
	if okRemote && okMerge {
		return &PushTarget{Branch: branch, Remote: remote, RemoteBranch: strings.TrimPrefix(merge, "refs/heads/")}, nil
	}
	out, err := r.Git("remote")
	if err != nil {
		return nil, err
	}
	remotes := lines(out)
	if len(remotes) == 0 {
		return nil, errors.New("no remote configured")
	}
	return &PushTarget{Branch: branch, Remote: remotes[0], RemoteBranch: branch, IsNew: true}, nil
}

// Outgoing returns the commits that a push of the current branch would
// send.
func (r *Repo) Outgoing(target *PushTarget) ([]Commit, error) {
	if target.IsNew {
		return r.LogRevs([]string{"HEAD", "--not", "--remotes"}, 1000)
	}
	return r.LogRevs([]string{target.Remote + "/" + target.RemoteBranch + "..HEAD"}, 1000)
}

// PushTo pushes the branch, and with tags every local tag too.
func (r *Repo) PushTo(target *PushTarget, force, tags bool) (string, error) {
	if force && IsProtected(target.RemoteBranch) {
		return "", errors.New(i18n.Tf("{} is a protected branch and is never force pushed (see Settings › Version Control)", target.RemoteBranch))
	}
	args := []string{"push"}
	if force {
		args = append(args, "--force-with-lease")
	}
	if tags {
		args = append(args, "--tags")
	}
	if target.IsNew {
		args = append(args, "-u")
	}
	return r.Git(append(args, target.Remote, target.Branch+":"+target.RemoteBranch)...)
}

func (r *Repo) Push() (string, error) {
	target, err := r.PushTarget()
	if err != nil {
		return "", err
	}
	return r.PushTo(target, false, false)
}

// PushBranch pushes local branch branch, current or not.
func (r *Repo) PushBranch(branch string) (string, error) {
	target, err := r.PushTargetFor(branch)
	if err != nil {
		return "", err
	}
	return r.PushTo(target, false, false)
}

// UpdateBranch fast-forwards a local branch that is not checked out to its
// upstream.
func (r *Repo) UpdateBranch(branch string) (string, error) {
	if current, _ := r.CurrentBranch(); current == branch {
		return "", fmt.Errorf("%s is checked out; use Update Project", branch)
	}
	target, err := r.PushTargetFor(branch)
	if err != nil {
		return "", err
	}
	if target.IsNew {
		return "", fmt.Errorf("%s has no tracked remote branch", branch)
	}
	// Without a leading +, fetch refuses anything but a fast-forward.
	return r.Git("fetch", target.Remote, target.RemoteBranch+":"+branch)
}

// SetUpstream makes branch track remote branch upstream (e.g. origin/main),
// or nothing when upstream is empty.
func (r *Repo) SetUpstream(branch, upstream string) (string, error) {
	if upstream == "" {
		return r.Git("branch", "--unset-upstream", branch)
	}
	return r.Git("branch", "--set-upstream-to="+upstream, branch)
}

// CreatePatch writes ids (oldest first) as one patch file, with their
// messages and authors, as git format-patch writes them.
func (r *Repo) CreatePatch(ids []string, dest string) error {
	var b strings.Builder
	for _, id := range ids {
		out, err := r.Git("format-patch", "--stdout", "--binary", "-1", id)
		if err != nil {
			return err
		}
		b.WriteString(out)
	}
	if err := os.WriteFile(dest, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", dest, err)
	}
	return nil
}

// ApplyPatch applies a patch file to the work tree; hunks that no longer
// fit are merged three-way and left with conflict markers.
func (r *Repo) ApplyPatch(file string) (string, error) {
	if out, err := r.Git("apply", "--binary", file); err == nil {
		return out, nil
	}
	out, err := r.Git("apply", "--3way", "--binary", file)
	if err != nil && strings.Contains(err.Error(), "with conflicts") {
		return "", nil
	}
	return out, err
}

// ApplyCommitChanges applies what commit id did to paths to the work tree,
// or undoes it when revert, without committing: JetBrains' "Cherry-Pick
// Selected Changes" and "Revert Selected Changes". Changes that no longer
// fit are merged three-way, and conflicts that leaves are an error. base is
// the parent the changes are taken against, "" for a root commit.
func (r *Repo) ApplyCommitChanges(id, base string, paths []string, revert bool) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("no files chosen")
	}
	args := []string{"diff-tree", "-p", "--binary", "--full-index", "-M"}
	if base != "" {
		args = append(args, base, id)
	} else {
		args = append(args, "--root", id)
	}
	out, err := r.Git(append(append(args, "--"), paths...)...)
	if err != nil {
		return "", err
	}
	// diff-tree starts with the commit's id when it shows several files.
	start := strings.Index(out, "diff --git")
	if start < 0 {
		return "", errors.New("the commit doesn't change these files")
	}
	p := out[start:]
	apply := []string{"apply", "--binary"}
	if revert {
		apply = append(apply, "-R")
	}
	if out, err := r.GitWithInput(p, append(slices.Clone(apply), "-")...); err == nil {
		return out, nil
	}
	return r.GitWithInput(p, append(apply, "--3way", "-")...)
}

// GetFromRevision replaces path in the work tree with its version in rev; a
// file rev doesn't have is deleted.
func (r *Repo) GetFromRevision(rev, path string) (string, error) {
	if r.ok("cat-file", "-e", rev+":"+path) {
		return r.Git("checkout", rev, "--", path)
	}
	out, err := r.Git("rm", "-q", "-f", "--ignore-unmatch", "--", path)
	if err != nil {
		return "", err
	}
	os.Remove(filepath.Join(r.root, path))
	return out, nil
}

// RebaseCommits returns the commits from from up to HEAD, oldest first, for
// an interactive rebase.
func (r *Repo) RebaseCommits(from string) ([]Commit, error) {
	if !r.ok("merge-base", "--is-ancestor", from, "HEAD") {
		return nil, errors.New("the commit is not on the current branch")
	}
	var commits []Commit
	var err error
	if r.ok("rev-parse", "--verify", "-q", from+"^") {
		commits, err = r.LogRevs([]string{"--topo-order", from + "^..HEAD"}, 100_000)
	} else {
		commits, err = r.LogRevs([]string{"HEAD"}, 100_000)
	}
	if err != nil {
		return nil, err
	}
	for _, c := range commits {
		if len(c.Parents) > 1 {
			return nil, errors.New("there are merge commits between this commit and HEAD; editing history across merges is not supported")
		}
	}
	slices.Reverse(commits)
	return commits, nil
}

// oldestOf is the oldest of ids, which must all be on the current branch
// with no merge commits between them and HEAD; history before it stays.
func (r *Repo) oldestOf(ids []string) (string, error) {
	base, ok := r.trimmed(append([]string{"merge-base", "--octopus"}, ids...)...)
	if !ok {
		return "", errors.New("the commits are not on the current branch")
	}
	if !slices.Contains(ids, base) {
		return "", errors.New("the commits are not on one line of history")
	}
	return base, nil
}

// RewriteBase is where rewrite starts: the commit rebased from. It checks
// that the rewrite is possible, before anything changes.
func (r *Repo) RewriteBase(rw *Rewrite) (string, error) {
	if r.State() != Clean {
		return "", errors.New("finish or abort the current merge, rebase or cherry-pick first")
	}
	if len(rw.IDs) == 0 {
		return "", errors.New("no commits selected")
	}
	base, err := r.oldestOf(rw.IDs)
	if err != nil {
		return "", err
	}
	if rw.Kind == RewriteFixupIntoParent {
		parent, ok := r.trimmed("rev-parse", "--verify", "-q", rw.IDs[0]+"^")
		if !ok {
			return "", errors.New("the first commit has no parent to meld into")
		}
		base = parent
	}
	// Checks ancestry and merges in the range.
	if _, err := r.RebaseCommits(base); err != nil {
		return "", err
	}
	return base, nil
}

// PushedTo is the upstream branch that already has id, if any: rewriting it
// then needs a force push.
func (r *Repo) PushedTo(id string) (string, bool) {
	upstream, ok := r.trimmed("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if !ok || !r.ok("merge-base", "--is-ancestor", id, "@{u}") {
		return "", false
	}
	return upstream, true
}

// Rewrite changes the current branch's history; see Rewrite.
func (r *Repo) Rewrite(rw *Rewrite) (string, error) {
	base, err := r.RewriteBase(rw)
	if err != nil {
		return "", err
	}
	if rw.Kind == RewriteReword {
		if head, _ := r.Head(); head == rw.IDs[0] {
			// Only the message changes: no rebase, and staged changes stay
			// staged.
			return r.GitWithInput(rw.Message, "commit", "--amend", "--only", "--allow-empty", "-q", "-F", "-")
		}
	}
	commits, err := r.RebaseCommits(base)
	if err != nil {
		return "", err
	}
	in := func(id string) bool { return slices.Contains(rw.IDs, id) }
	var plan []RebaseStep
	switch rw.Kind {
	case RewriteReword:
		for _, c := range commits {
			action := RebaseAction{Kind: Pick}
			if c.ID == rw.IDs[0] {
				action = RebaseAction{Kind: Reword, Message: rw.Message}
			}
			plan = append(plan, RebaseStep{c.ID, action})
		}
	case RewriteDrop:
		if !slices.ContainsFunc(commits, func(c Commit) bool { return !in(c.ID) }) {
			return "", errors.New("dropping every commit of the branch is not possible this way; delete the branch instead")
		}
		for _, c := range commits {
			action := RebaseAction{Kind: Pick}
			if in(c.ID) {
				action.Kind = Drop
			}
			plan = append(plan, RebaseStep{c.ID, action})
		}
	case RewriteFixupIntoParent:
		for _, c := range commits {
			action := RebaseAction{Kind: Pick}
			if c.ID == rw.IDs[0] {
				action.Kind = Fixup
			}
			plan = append(plan, RebaseStep{c.ID, action})
		}
	case RewriteSquash:
		// The oldest takes the new message; the others follow it as
		// fixups, keeping that message, and leave their old places.
		plan = append(plan, RebaseStep{base, RebaseAction{Kind: Reword, Message: rw.Message}})
		for _, c := range commits {
			if c.ID != base && in(c.ID) {
				plan = append(plan, RebaseStep{c.ID, RebaseAction{Kind: Fixup}})
			}
		}
		for _, c := range commits {
			if !in(c.ID) {
				plan = append(plan, RebaseStep{c.ID, RebaseAction{Kind: Pick}})
			}
		}
	}
	return r.InteractiveRebase(base, plan)
}

// UndoCommit undoes the last commit, keeping its changes staged; it returns
// its message for the next commit.
func (r *Repo) UndoCommit() (string, error) {
	if r.State() != Clean {
		return "", errors.New("finish or abort the current merge, rebase or cherry-pick first")
	}
	message, err := r.CommitMessage("HEAD")
	if err != nil {
		return "", err
	}
	if r.ok("rev-parse", "--verify", "-q", "HEAD^") {
		_, err = r.Git("reset", "--soft", "HEAD^")
	} else {
		// The first commit: the branch goes back to having none.
		_, err = r.Git("update-ref", "-d", "HEAD")
	}
	return message, err
}

// InteractiveRebase runs git rebase -i with a prepared todo list instead of
// an editor.
func (r *Repo) InteractiveRebase(from string, plan []RebaseStep) (string, error) {
	if len(plan) > 0 && (plan[0].Action.Kind == Squash || plan[0].Action.Kind == Fixup) {
		return "", errors.New("the first commit cannot be squashed or fixed up")
	}
	dir := r.rebaseFilesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var todo strings.Builder
	for ix, step := range plan {
		switch step.Action.Kind {
		case Pick:
			fmt.Fprintf(&todo, "pick %s\n", step.ID)
		case Squash:
			fmt.Fprintf(&todo, "squash %s\n", step.ID)
		case Fixup:
			fmt.Fprintf(&todo, "fixup %s\n", step.ID)
		case Drop:
			fmt.Fprintf(&todo, "drop %s\n", step.ID)
		case Edit:
			fmt.Fprintf(&todo, "edit %s\n", step.ID)
		case Reword:
			file := filepath.Join(dir, fmt.Sprintf("message-%d", ix))
			if err := os.WriteFile(file, []byte(step.Action.Message), 0o644); err != nil {
				return "", err
			}
			fmt.Fprintf(&todo, "pick %s\n", step.ID)
			fmt.Fprintf(&todo, "exec git commit --amend --allow-empty -q -F '%s'\n", filepath.ToSlash(file))
		}
	}
	todoFile := filepath.Join(dir, "todo")
	if err := os.WriteFile(todoFile, []byte(todo.String()), 0o644); err != nil {
		return "", err
	}
	args := []string{"-c", "sequence.editor=cp '" + filepath.ToSlash(todoFile) + "'", "-c", "core.editor=true", "rebase", "-i", "--autostash"}
	if r.ok("rev-parse", "--verify", "-q", from+"^") {
		args = append(args, from+"^")
	} else {
		args = append(args, "--root")
	}
	out, err := r.Git(args...)
	// A rebase stopped on a conflict still needs the reworded messages.
	if r.State() == Clean {
		os.RemoveAll(dir)
	}
	return out, err
}

// CompareRefs returns the commits in branch that base lacks, and the
// commits in base that branch lacks.
func (r *Repo) CompareRefs(branch, base string) (incoming, outgoing []Commit, err error) {
	for _, rev := range []string{branch, base} {
		if !r.ok("rev-parse", "--verify", "-q", "--end-of-options", rev+"^{commit}") {
			return nil, nil, errors.New(i18n.Tf("No commit named {}", rev))
		}
	}
	if incoming, err = r.LogRevs([]string{base + ".." + branch}, 10_000); err != nil {
		return nil, nil, err
	}
	outgoing, err = r.LogRevs([]string{branch + ".." + base}, 10_000)
	return incoming, outgoing, err
}

func (r *Repo) Remotes() ([]Remote, error) {
	out, err := r.Git("remote", "-v")
	if err != nil {
		return nil, err
	}
	var remotes []Remote
	for _, line := range lines(out) {
		// e.g. "origin\thttps://example.com/repo.git (fetch)"
		name, rest, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		ix := strings.LastIndexByte(rest, ' ')
		if ix < 0 {
			continue
		}
		url, kind := rest[:ix], rest[ix+1:]
		at := slices.IndexFunc(remotes, func(r Remote) bool { return r.Name == name })
		if at < 0 {
			remotes = append(remotes, Remote{Name: name})
			at = len(remotes) - 1
		}
		switch kind {
		case "(fetch)":
			remotes[at].FetchURL = url
		case "(push)":
			remotes[at].PushURL = url
		}
	}
	return remotes, nil
}

func (r *Repo) AddRemote(name, url string) (string, error) {
	return r.Git("remote", "add", "--", name, url)
}

func (r *Repo) SetRemoteURL(name, url string) (string, error) {
	return r.Git("remote", "set-url", "--", name, url)
}

func (r *Repo) RenameRemote(old, new string) (string, error) {
	return r.Git("remote", "rename", "--", old, new)
}

func (r *Repo) RemoveRemote(name string) (string, error) {
	return r.Git("remote", "remove", "--", name)
}

func (r *Repo) Stashes() ([]Stash, error) {
	out, err := r.Git("stash", "list", "--format=%gd"+field+"%H"+field+"%P"+field+"%an"+field+"%ae"+field+"%at"+field+"%s")
	if err != nil {
		return nil, err
	}
	var stashes []Stash
	for _, line := range lines(out) {
		f := strings.Split(line, field)
		if len(f) < 6 {
			continue
		}
		time, _ := strconv.ParseInt(f[5], 10, 64)
		message := ""
		if len(f) > 6 {
			message = f[6]
		}
		stashes = append(stashes, Stash{
			Name:    f[0],
			Message: message,
			Commit:  Commit{ID: f[1], Parents: strings.Fields(f[2]), Author: f[3], Email: f[4], Time: time, Subject: message},
		})
	}
	return stashes, nil
}

// UpdateCommitGraph keeps git's commit-graph current: it writes one (with
// changed-path filters) when the repository has none, else adds the commits
// made since as a new layer. History walks, branch lookups and file history
// are many times faster with it; git gc and git maintenance write the same
// file.
func (r *Repo) UpdateCommitGraph() error {
	args := []string{"commit-graph", "write", "--reachable", "--changed-paths"}
	if r.HasCommitGraph() {
		args = append(args, "--split")
	}
	_, err := r.Git(args...)
	return err
}

// WriteQuickCommitGraph writes a first commit-graph, quickly: without the
// changed paths, which take minutes on a big history. AddChangedPaths adds
// them.
func (r *Repo) WriteQuickCommitGraph() error {
	_, err := r.Git("commit-graph", "write", "--reachable")
	return err
}

func (r *Repo) AddChangedPaths() error {
	_, err := r.Git("commit-graph", "write", "--reachable", "--changed-paths", "--split=replace")
	return err
}

// HasCommitGraph reports whether git has written a commit-graph, without
// which ordering the log means walking the whole history first.
func (r *Repo) HasCommitGraph() bool {
	info, ok := r.trimmed("rev-parse", "--git-path", "objects/info")
	if !ok {
		return false
	}
	info = filepath.FromSlash(info)
	if !filepath.IsAbs(info) {
		info = filepath.Join(r.root, info)
	}
	return exists(filepath.Join(info, "commit-graph")) || exists(filepath.Join(info, "commit-graphs"))
}

// RefreshIndex brings the file times cached in the index up to date. Status
// then stops re-reading files whose contents didn't change, which it would
// otherwise do on every refresh.
//
// This lets git status take the index lock it calls optional, which it
// skips when anything else holds it, so it never gets in the way.
func (r *Repo) RefreshIndex() error {
	cmd := command(r.root, []string{"status", "--porcelain", "--untracked-files=no"})
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git status: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Worktrees returns the repository's working trees, the main one first.
func (r *Repo) Worktrees() ([]Worktree, error) {
	current := canonical(r.root)
	out, err := r.Git("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var trees []Worktree
	for _, block := range strings.Split(out, "\n\n") {
		var tree *Worktree
		for _, line := range lines(block) {
			key, value, _ := strings.Cut(line, " ")
			switch {
			case key == "worktree":
				path := filepath.FromSlash(value)
				tree = &Worktree{Path: path, IsMain: len(trees) == 0, IsCurrent: canonical(path) == current}
			case tree == nil:
			case key == "HEAD":
				tree.Head = value
			case key == "branch":
				tree.Branch = strings.TrimPrefix(value, "refs/heads/")
			case key == "locked":
				tree.Locked = true
			case key == "prunable":
				tree.Prunable = true
			}
		}
		if tree != nil {
			trees = append(trees, *tree)
		}
	}
	return trees, nil
}

func canonical(path string) string {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p
	}
	return path
}

// AddWorktree adds a working tree at path with branch checked out: an
// existing local branch, a new one tracking the remote branch of that name,
// or else a new branch from HEAD.
func (r *Repo) AddWorktree(path, branch string) (*Repo, error) {
	hasRemote := false
	if out, err := r.Git("for-each-ref", "--format=%(refname)", "refs/remotes/*/"+branch); err == nil {
		hasRemote = strings.TrimSpace(out) != ""
	}
	var err error
	if r.ok("rev-parse", "--verify", "-q", "refs/heads/"+branch) || hasRemote {
		// git creates a tracking branch from a remote branch of that name.
		_, err = r.Git("worktree", "add", path, branch)
	} else {
		_, err = r.Git("worktree", "add", "-b", branch, path)
	}
	if err != nil {
		return nil, err
	}
	return Open(path)
}

// WorktreeIsDirty reports whether the working tree at path has local
// changes.
func WorktreeIsDirty(path string) bool {
	out, err := run(path, []string{"status", "--porcelain"}, false)
	return err == nil && strings.TrimSpace(out) != ""
}

// RemoveWorktree removes a working tree; with force even one with local
// changes.
func (r *Repo) RemoveWorktree(path string, force bool) (string, error) {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	return r.Git(append(args, path)...)
}

// PruneWorktrees forgets working trees whose folders are gone.
func (r *Repo) PruneWorktrees() (string, error) { return r.Git("worktree", "prune") }

func (r *Repo) Submodules() ([]Submodule, error) {
	out, err := r.Git("submodule", "status")
	if err != nil {
		return nil, err
	}
	var subs []Submodule
	for _, line := range lines(out) {
		if line == "" {
			continue
		}
		state := SubmoduleUpToDate
		switch line[0] {
		case '-':
			state = SubmoduleUninitialized
		case '+':
			state = SubmoduleOutOfDate
		case 'U':
			state = SubmoduleConflict
		}
		f := strings.Fields(line[1:])
		if len(f) < 2 {
			continue
		}
		subs = append(subs, Submodule{Path: f[1], Commit: f[0], State: state})
	}
	return subs, nil
}

// UpdateSubmodules clones missing submodules and checks all of them (or
// paths) out at their recorded commits, nested ones too.
func (r *Repo) UpdateSubmodules(paths []string) (string, error) {
	return r.Git(append([]string{"submodule", "update", "--init", "--recursive", "--"}, paths...)...)
}

// FavoriteBranches returns the full names (refs/heads/main) of the branches
// marked as favorites; until any is marked, main and master are.
func (r *Repo) FavoriteBranches() []string {
	file, err := r.favoritesFile()
	if err == nil {
		text, err := os.ReadFile(file)
		if err == nil {
			var out []string
			for _, l := range lines(string(text)) {
				if l = strings.TrimSpace(l); l != "" {
					out = append(out, l)
				}
			}
			return out
		}
	}
	return []string{"refs/heads/main", "refs/heads/master"}
}

func (r *Repo) SetFavoriteBranch(fullName string, favorite bool) error {
	favorites := slices.DeleteFunc(r.FavoriteBranches(), func(f string) bool { return f == fullName })
	if favorite {
		favorites = append(favorites, fullName)
	}
	file, err := r.favoritesFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	var text strings.Builder
	for _, f := range favorites {
		text.WriteString(f + "\n")
	}
	return os.WriteFile(file, []byte(text.String()), 0o644)
}

// favoritesFile is shared by all worktrees of the repository.
func (r *Repo) favoritesFile() (string, error) {
	common, ok := r.trimmed("rev-parse", "--path-format=absolute", "--git-common-dir")
	if !ok {
		return "", errors.New("no git directory")
	}
	return filepath.Join(filepath.FromSlash(common), "banyan", "favorite-branches"), nil
}

// RecentBranches returns local branches checked out lately, most recent
// first, from HEAD's reflog.
func (r *Repo) RecentBranches(count int) []string {
	out, err := r.Git("reflog", "show", "--format=%gs", "-n", "300", "HEAD", "--")
	if err != nil {
		return nil
	}
	localsOut, err := r.Git("for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil
	}
	locals := map[string]bool{}
	for _, l := range lines(localsOut) {
		locals[l] = true
	}
	var recent []string
	for _, line := range lines(out) {
		moves, ok := strings.CutPrefix(line, "checkout: moving from ")
		if !ok {
			continue
		}
		ix := strings.LastIndex(moves, " to ")
		if ix < 0 {
			continue
		}
		// The branch left counts too: it was in use until then.
		for _, name := range []string{moves[ix+4:], moves[:ix]} {
			if !slices.Contains(recent, name) && locals[name] {
				recent = append(recent, name)
			}
		}
		if len(recent) >= count {
			return recent[:count]
		}
	}
	return recent
}

func (r *Repo) Stash(options *StashOptions) (string, error) {
	args := []string{"stash", "push"}
	if options.IncludeUntracked {
		args = append(args, "--include-untracked")
	}
	if options.KeepIndex {
		args = append(args, "--keep-index")
	}
	if options.Message != "" {
		args = append(args, "-m", options.Message)
	}
	if len(options.Paths) > 0 {
		args = append(append(args, "--"), options.Paths...)
	}
	return r.Git(args...)
}

// StashBranch checks out a new branch at the commit stash was made on and
// applies it there, index included, dropping it when that works.
func (r *Repo) StashBranch(stash, branch string) (string, error) {
	return r.Git("stash", "branch", branch, stash)
}

func (r *Repo) StashPop(name string) (string, error)   { return r.Git("stash", "pop", name) }
func (r *Repo) StashApply(name string) (string, error) { return r.Git("stash", "apply", name) }
func (r *Repo) StashDrop(name string) (string, error)  { return r.Git("stash", "drop", name) }
