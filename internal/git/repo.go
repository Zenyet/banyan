package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Zenyet/banyan/internal/i18n"
)

type Repo struct {
	root string
	// gitDir is the .git directory, which stays put for as long as the
	// repository.
	gitDir string
	// recorded puts the commands run in the Git console.
	recorded bool
}

func Open(path string) (*Repo, error) {
	out, err := run(path, []string{"rev-parse", "--show-toplevel", "--absolute-git-dir"}, false)
	if err != nil {
		return nil, fmt.Errorf("%s is not a git repository: %w", path, err)
	}
	ls := lines(out)
	if len(ls) < 2 {
		return nil, fmt.Errorf("%s is not a git repository", path)
	}
	return &Repo{root: filepath.FromSlash(ls[0]), gitDir: filepath.FromSlash(ls[1])}, nil
}

// Init creates a repository in dir.
func Init(dir string) (*Repo, error) {
	if _, err := run(dir, []string{"init"}, false); err != nil {
		return nil, err
	}
	return Open(dir)
}

// Clone clones url into dest, which must not exist or be empty.
func Clone(url, dest string) (*Repo, error) {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	if _, err := run(parent, []string{"clone", "--", url, dest}, true); err != nil {
		return nil, err
	}
	return Open(dest)
}

// CloneDirName is the directory name git clone would pick for url.
func CloneDirName(url string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(url), "/")
	last := trimmed[strings.LastIndexAny(trimmed, "/:")+1:]
	return strings.TrimSuffix(last, ".git")
}

// Recorded returns the repository with the commands it runs shown in the
// Git console, for the operations of user actions.
func (r *Repo) Recorded() *Repo {
	c := *r
	c.recorded = true
	return &c
}

func (r *Repo) Root() string   { return r.root }
func (r *Repo) GitDir() string { return r.gitDir }

func (r *Repo) Name() string {
	if name := filepath.Base(r.root); name != "" && name != "." && name != string(filepath.Separator) {
		return name
	}
	return r.root
}

func (r *Repo) Git(args ...string) (string, error) {
	return run(r.root, args, r.recorded)
}

// GitWithInput runs git with input on stdin.
func (r *Repo) GitWithInput(input string, args ...string) (string, error) {
	return runInput(r.root, args, []byte(input), r.recorded)
}

func (r *Repo) ok(args ...string) bool {
	_, err := r.Git(args...)
	return err == nil
}

func (r *Repo) trimmed(args ...string) (string, bool) {
	out, err := r.Git(args...)
	if err != nil {
		return "", false
	}
	out = strings.TrimSpace(out)
	return out, out != ""
}

// escapeBasicRegex returns text as a POSIX basic regular expression
// matching itself.
func escapeBasicRegex(text string) string {
	var b strings.Builder
	for _, c := range text {
		if strings.ContainsRune(`\.[]*^$`, c) {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Log returns commits of the filter's refs (all branches, remotes and tags
// by default), newest first. Unlike --since, the date filter looks past old
// commits that sit above newer ones.
func (r *Repo) Log(filter *LogFilter, limit int) ([]Commit, error) {
	var options []string
	if filter.Since != "" {
		options = append(options, "--since-as-filter="+filter.Since)
	}
	if len(filter.Authors) > 0 || filter.Text != "" {
		// Explicitly basic, whatever grep.patternType says.
		options = append(options, "--basic-regexp")
	}
	// Several --author options match any of them.
	for _, author := range filter.Authors {
		options = append(options, "--author=^"+escapeBasicRegex(author)+" <")
	}
	if filter.Text != "" {
		options = append(options, "--regexp-ignore-case", "--grep="+escapeBasicRegex(filter.Text))
	}
	// Finding the tags off branches is quick only with a commit-graph, and
	// only putting the log in order needs it.
	ordered := r.HasCommitGraph()
	var revs []string
	switch {
	case filter.Refs != "":
		revs = []string{filter.Refs}
	case ordered:
		// Not --tags: old tags make git walk the history down to them to
		// put it in order. Commits on branches show their tags anyway.
		revs = append([]string{"--branches", "--remotes", "HEAD"}, r.tagsOffBranches()...)
	default:
		revs = []string{"--branches", "--remotes", "--tags", "HEAD"}
	}
	revs = append(revs, options...)
	return r.logPaths(revs, filter.Paths, limit)
}

// tagsOffBranches returns tags of commits that no branch, remote branch or
// HEAD has.
func (r *Repo) tagsOffBranches() []string {
	branches, err := r.Git("for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil
	}
	args := []string{"for-each-ref", "--format=%(objecttype) %(*objecttype) %(refname)", "--no-merged=HEAD"}
	for _, b := range lines(branches) {
		if !strings.HasSuffix(b, "/HEAD") {
			args = append(args, "--no-merged="+b)
		}
	}
	args = append(args, "refs/tags")
	out, err := r.Git(args...)
	if err != nil {
		return nil
	}
	var tags []string
	for _, line := range lines(out) {
		ix := strings.LastIndexByte(line, ' ')
		if ix < 0 {
			continue
		}
		// A tag of a tree or blob has no history.
		switch strings.TrimSpace(line[:ix]) {
		case "commit", "tag commit":
			tags = append(tags, line[ix+1:])
		}
	}
	return tags
}

// WebRemote is the web page of the remote the current branch tracks, else
// origin, else the first remote.
func (r *Repo) WebRemote() (WebRemote, bool) {
	name, ok := r.DefaultRemote()
	if !ok {
		return WebRemote{}, false
	}
	url, err := r.Git("remote", "get-url", name)
	if err != nil {
		return WebRemote{}, false
	}
	return ParseWebRemote(url)
}

// DefaultRemote is the remote the current branch tracks, else origin, else
// the first one.
func (r *Repo) DefaultRemote() (string, bool) {
	tracked := ""
	if branch, ok := r.CurrentBranch(); ok {
		tracked, _ = r.trimmed("config", "--get", "branch."+branch+".remote")
	}
	out, err := r.Git("remote")
	if err != nil {
		return "", false
	}
	names := lines(out)
	for _, n := range names {
		if tracked != "" && n == tracked {
			return n, true
		}
	}
	for _, n := range names {
		if n == "origin" {
			return n, true
		}
	}
	if len(names) > 0 {
		return names[0], true
	}
	return "", false
}

// LocateCommit returns the commit a hash, branch, tag or other revision
// names, and how many commits of the whole log are not its ancestors: an
// upper bound on how far down the log it is.
func (r *Repo) LocateCommit(rev string) (string, int, error) {
	rev = strings.TrimSpace(rev)
	id, ok := r.trimmed("rev-parse", "--verify", "-q", "--end-of-options", rev+"^{commit}")
	if !ok {
		return "", 0, errors.New(i18n.Tf("No commit named {}", rev))
	}
	out, err := r.Git("rev-list", "--count", "--branches", "--remotes", "--tags", "HEAD", "--not", id)
	if err != nil {
		return "", 0, err
	}
	above, _ := strconv.Atoi(strings.TrimSpace(out))
	return id, above, nil
}

// UserName is the configured user.name, which "Me" in the log's user filter
// stands for.
func (r *Repo) UserName() string {
	name, _ := r.trimmed("config", "--get", "user.name")
	return name
}

// LogRevs runs git log over arbitrary revision arguments, newest first.
func (r *Repo) LogRevs(revs []string, limit int) ([]Commit, error) {
	return r.logPaths(revs, nil, limit)
}

func (r *Repo) logPaths(revs, paths []string, limit int) ([]Commit, error) {
	format := "--format=%H" + field + "%P" + field + "%an" + field + "%ae" + field + "%at" + field + "%D" + field + "%s" + record
	args := []string{"log", "--decorate=full", format, fmt.Sprintf("-n%d", limit)}
	// Without a commit-graph, --date-order reads every commit before
	// printing one: seconds on a big repository. Until git has written one,
	// take the commits in plain date order, which only puts a commit below
	// its parent when their clocks disagree.
	if r.HasCommitGraph() {
		args = append([]string{"log", "--date-order"}, args[1:]...)
	}
	args = append(args, revs...)
	args = append(args, "--")
	args = append(args, paths...)
	out, err := r.Git(args...)
	if err != nil {
		// A fresh repository has no HEAD yet.
		if !r.HasHead() {
			return nil, nil
		}
		return nil, err
	}
	var commits []Commit
	for _, rec := range strings.Split(out, record) {
		f := strings.Split(strings.TrimLeft(rec, "\n"), field)
		if len(f) < 6 || f[0] == "" {
			continue
		}
		time, _ := strconv.ParseInt(f[4], 10, 64)
		subject := ""
		if len(f) > 6 {
			subject = f[6]
		}
		commits = append(commits, Commit{
			ID:      f[0],
			Parents: strings.Fields(f[1]),
			Author:  f[2],
			Email:   f[3],
			Time:    time,
			Refs:    parseDecorations(f[5]),
			Subject: subject,
		})
	}
	return commits, nil
}

// FileHistory returns commits that changed path, following renames.
//
// Not git log --follow, which can't use the commit-graph's changed paths
// and so reads every commit's tree: seconds on a big history. The history
// of each name is quick; where it begins, a rename is looked for in that
// one commit.
func (r *Repo) FileHistory(path string) ([]FileRevision, error) {
	var revisions []FileRevision
	names := map[string]bool{path: true}
	from := ""
	for {
		segment, err := r.pathHistory(from, path)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, segment...)
		if len(segment) == 0 {
			break
		}
		origin := segment[len(segment)-1].Commit
		if len(origin.Parents) == 0 {
			break
		}
		parent := origin.Parents[0]
		old, ok := r.renamedFrom(parent, origin.ID, path)
		if !ok || names[old] {
			break
		}
		names[old] = true
		path, from = old, parent
	}
	return revisions, nil
}

// pathHistory returns commits from from (HEAD by default) that changed
// path.
func (r *Repo) pathHistory(from, path string) ([]FileRevision, error) {
	format := "--format=" + record + "%H" + field + "%P" + field + "%an" + field + "%ae" + field + "%at" + field + "%s"
	// Like --follow, which shows no merges.
	args := []string{"log", "--no-merges", format}
	if from != "" {
		args = append(args, from)
	}
	args = append(args, "--", path)
	out, err := r.Git(args...)
	if err != nil {
		return nil, err
	}
	var revisions []FileRevision
	for _, rec := range strings.Split(out, record) {
		f := strings.Split(strings.TrimRight(rec, "\n"), field)
		if len(f) < 5 || f[0] == "" {
			continue
		}
		time, _ := strconv.ParseInt(f[4], 10, 64)
		subject := ""
		if len(f) > 5 {
			subject = f[5]
		}
		revisions = append(revisions, FileRevision{
			Commit: Commit{ID: f[0], Parents: strings.Fields(f[1]), Author: f[2], Email: f[3], Time: time, Subject: subject},
			Path:   path,
		})
	}
	return revisions, nil
}

// renamedFrom is the name path had in parent, if commit id renamed it.
func (r *Repo) renamedFrom(parent, id, path string) (string, bool) {
	// Still there before: changed, not renamed.
	if r.ok("cat-file", "-e", parent+":"+path) {
		return "", false
	}
	out, err := r.Git("diff-tree", "-r", "-M", "--name-status", "-z", parent, id)
	if err != nil {
		return "", false
	}
	f := strings.Split(out, "\x00")
	for i := 0; i < len(f); i++ {
		status := f[i]
		switch {
		case strings.HasPrefix(status, "R"):
			if i+2 >= len(f) {
				return "", false
			}
			if f[i+2] == path {
				return f[i+1], true
			}
			i += 2
		case strings.HasPrefix(status, "C"):
			i += 2
		default:
			i++
		}
	}
	return "", false
}

func whitespaceArgs() []string {
	if IgnoreWhitespace() {
		return []string{"-w"}
	}
	return nil
}

// RevisionDiff is the patch of path introduced by commit id (against its
// first parent).
func (r *Repo) RevisionDiff(id, path string) (string, error) {
	args := append([]string{"show", "--format=", "-M", "--first-parent"}, whitespaceArgs()...)
	return r.Git(append(args, id, "--", path)...)
}

// Blame returns the line-by-line blame of path at rev.
func (r *Repo) Blame(rev, path string) ([]BlameLine, error) {
	out, err := r.Git("blame", "--porcelain", rev, "--", path)
	if err != nil {
		return nil, err
	}
	// git describes each commit once, at its first line.
	infos := map[string]BlameLine{}
	var result []BlameLine
	var current BlameLine
	for _, line := range lines(out) {
		if text, ok := strings.CutPrefix(line, "\t"); ok {
			info, seen := infos[current.ID]
			if !seen {
				info = current
				infos[current.ID] = info
			}
			info.OrigLine = current.OrigLine
			info.Text = text
			result = append(result, info)
		} else if v, ok := strings.CutPrefix(line, "author "); ok {
			current.Author = v
		} else if v, ok := strings.CutPrefix(line, "author-time "); ok {
			current.Time, _ = strconv.ParseInt(v, 10, 64)
		} else if v, ok := strings.CutPrefix(line, "summary "); ok {
			current.Summary = v
		} else if v, ok := strings.CutPrefix(line, "filename "); ok {
			current.Path = v
		} else if v, ok := strings.CutPrefix(line, "previous "); ok {
			current.Previous, current.PreviousPath, _ = strings.Cut(v, " ")
		} else {
			fields := strings.Split(line, " ")
			id := fields[0]
			if len(id) == 40 && isHex(id) {
				if id != current.ID {
					if info, ok := infos[id]; ok {
						current = info
					} else {
						current = BlameLine{ID: id}
					}
				}
				current.OrigLine = 0
				if len(fields) > 1 {
					current.OrigLine, _ = strconv.Atoi(fields[1])
				}
			}
		}
	}
	return result, nil
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// Head is the commit HEAD points at, if any.
func (r *Repo) Head() (string, bool) {
	return r.trimmed("rev-parse", "-q", "--verify", "HEAD")
}

// NotIgnored returns the subset of paths (inside the work tree) that git
// does not ignore.
func (r *Repo) NotIgnored(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var input strings.Builder
	for _, p := range paths {
		input.WriteString(p)
		input.WriteByte(0)
	}
	cmd := command(r.root, []string{"check-ignore", "--stdin", "-z"})
	cmd.Stdin = strings.NewReader(input.String())
	// check-ignore exits with 1 when nothing is ignored.
	out, _ := cmd.Output()
	ignored := map[string]bool{}
	for _, p := range nonEmpty(string(out), "\x00") {
		ignored[p] = true
	}
	var kept []string
	for _, p := range paths {
		if !ignored[p] {
			kept = append(kept, p)
		}
	}
	return kept, nil
}

func (r *Repo) HasHead() bool {
	return r.ok("rev-parse", "--verify", "-q", "HEAD")
}

func (r *Repo) CommitDetails(id string) (*CommitDetails, error) {
	return r.CommitDetailsAgainst(id, 0)
}

// CommitDetailsAgainst returns details with the changes against parent
// base (counting from 0).
func (r *Repo) CommitDetailsAgainst(id string, base int) (*CommitDetails, error) {
	format := "--format=%H" + field + "%P" + field + "%an" + field + "%ae" + field + "%at" + field + "%cn" + field + "%ct" + field + "%G?" + field + "%GS" + field + "%B"
	out, err := r.Git("show", "-s", format, id)
	if err != nil {
		return nil, err
	}
	f := strings.SplitN(out, field, 10)
	for len(f) < 10 {
		f = append(f, "")
	}
	d := &CommitDetails{
		ID:        f[0],
		Parents:   strings.Fields(f[1]),
		Author:    f[2],
		Email:     f[3],
		Committer: f[5],
		Signature: parseSignature(f[7], f[8]),
		Message:   strings.TrimRight(f[9], " \t\r\n"),
	}
	d.AuthorTime, _ = strconv.ParseInt(f[4], 10, 64)
	d.CommitTime, _ = strconv.ParseInt(f[6], 10, 64)
	d.Base = min(base, max(len(d.Parents)-1, 0))
	d.Files, err = r.commitFiles(d.ID, d.BaseParent())
	if err != nil {
		return nil, err
	}
	if out, err := r.Git("branch", "-a", "--format=%(refname:short)", "--contains", d.ID); err == nil {
		d.Branches = lines(out)
	}
	return d, nil
}

// commitFiles returns the files changed by id against parent.
func (r *Repo) commitFiles(id, parent string) ([]FileChange, error) {
	var out string
	var err error
	if parent != "" {
		out, err = r.Git("diff-tree", "-r", "-M", "--name-status", "-z", parent, id)
	} else {
		out, err = r.Git("diff-tree", "-r", "-M", "--name-status", "-z", "--root", id)
	}
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out), nil
}

// parseNameStatus parses --name-status -z output.
func parseNameStatus(out string) []FileChange {
	parts := nonEmpty(out, "\x00")
	var files []FileChange
	for i := 0; i < len(parts); i++ {
		status := parts[i]
		// With --root the first token is the commit id itself.
		if len(status) >= 40 && isHex(status) {
			continue
		}
		next := func() string {
			if i+1 < len(parts) {
				i++
				return parts[i]
			}
			return ""
		}
		letter := status[0]
		if letter == 'R' || letter == 'C' {
			orig := next()
			files = append(files, FileChange{Status: letter, Path: next(), OrigPath: orig})
		} else {
			files = append(files, FileChange{Status: letter, Path: next()})
		}
	}
	return files
}

// ChangesBetween returns the files that differ from from to to, or to the
// work tree when to is empty.
func (r *Repo) ChangesBetween(from, to string) ([]FileChange, error) {
	args := []string{"diff", "-M", "--name-status", "-z", from}
	if to != "" {
		args = append(args, to)
	}
	out, err := r.Git(append(args, "--")...)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out), nil
}

// DiffBetween is the patch of one file from from to to, or to the work tree
// when to is empty.
func (r *Repo) DiffBetween(from, to string, file *FileChange) (string, error) {
	args := append([]string{"diff", "-M"}, whitespaceArgs()...)
	args = append(args, from)
	if to != "" {
		args = append(args, to)
	}
	args = append(args, "--", file.Path)
	if file.OrigPath != "" {
		args = append(args, file.OrigPath)
	}
	return r.Git(args...)
}

// CommitFileDiff is the patch of one file in a commit, against the parent
// its details compare with.
func (r *Repo) CommitFileDiff(d *CommitDetails, file *FileChange) (string, error) {
	var args []string
	if parent := d.BaseParent(); parent != "" {
		args = append(append([]string{"diff", "-M"}, whitespaceArgs()...), parent, d.ID)
	} else {
		args = append(append([]string{"show", "--format=", "-M"}, whitespaceArgs()...), d.ID)
	}
	args = append(args, "--", file.Path)
	if file.OrigPath != "" {
		args = append(args, file.OrigPath)
	}
	return r.Git(args...)
}

func (r *Repo) CurrentBranch() (string, bool) {
	return r.trimmed("symbolic-ref", "--short", "-q", "HEAD")
}

func (r *Repo) Branches() ([]Branch, error) {
	format := "--format=%(refname)" + field + "%(objectname)" + field + "%(*objectname)" + field + "%(upstream:short)" + field + "%(upstream:track,nobracket)" + field + "%(HEAD)"
	out, err := r.Git("for-each-ref", format, "refs/heads", "refs/remotes", "refs/tags")
	if err != nil {
		return nil, err
	}
	var branches []Branch
	for _, line := range lines(out) {
		f := strings.Split(line, field)
		for len(f) < 6 {
			f = append(f, "")
		}
		refname := f[0]
		// An annotated tag points at its commit through the tag object.
		target := f[2]
		if target == "" {
			target = f[1]
		}
		var kind RefKind
		var name string
		if n, ok := strings.CutPrefix(refname, "refs/heads/"); ok {
			kind, name = RefLocal, n
		} else if n, ok := strings.CutPrefix(refname, "refs/remotes/"); ok {
			if strings.HasSuffix(n, "/HEAD") {
				continue
			}
			kind, name = RefRemote, n
		} else if n, ok := strings.CutPrefix(refname, "refs/tags/"); ok {
			kind, name = RefTag, n
		} else {
			continue
		}
		branches = append(branches, Branch{Name: name, Kind: kind, Target: target, Upstream: f[3], Track: f[4], IsHead: f[5] == "*"})
	}
	return branches, nil
}

func (r *Repo) Status() ([]StatusEntry, error) {
	out, err := r.Git("status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	parts := nonEmpty(out, "\x00")
	var entries []StatusEntry
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if len(part) < 4 {
			continue
		}
		e := StatusEntry{Index: part[0], Worktree: part[1], Path: part[3:]}
		if (e.Index == 'R' || e.Index == 'C') && i+1 < len(parts) {
			i++
			e.OrigPath = parts[i]
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// WorktreeDiff is the patch of a working tree file against HEAD (staged and
// unstaged together).
func (r *Repo) WorktreeDiff(e *StatusEntry, ignoreWhitespace bool) (string, error) {
	if e.IsUntracked() {
		return runAllowDiff(r.root, []string{"diff", "--no-index", "--", os.DevNull, e.Path})
	}
	args := []string{"diff", "-M"}
	if ignoreWhitespace {
		args = append(args, "-w")
	}
	if r.HasHead() {
		args = append(args, "HEAD")
	} else {
		args = append(args, "--cached")
	}
	args = append(args, "--", e.Path)
	if e.OrigPath != "" {
		args = append(args, e.OrigPath)
	}
	return r.Git(args...)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (r *Repo) State() RepoState {
	switch {
	case exists(filepath.Join(r.gitDir, "rebase-merge")) || exists(filepath.Join(r.gitDir, "rebase-apply")):
		return Rebasing
	case exists(filepath.Join(r.gitDir, "MERGE_HEAD")):
		return Merging
	case exists(filepath.Join(r.gitDir, "CHERRY_PICK_HEAD")):
		return CherryPicking
	case exists(filepath.Join(r.gitDir, "REVERT_HEAD")):
		return Reverting
	}
	return Clean
}

// MergeMessage is the prepared message of an in-progress merge, cherry-pick
// or revert.
func (r *Repo) MergeMessage() string {
	text, err := os.ReadFile(filepath.Join(r.gitDir, "MERGE_MSG"))
	if err != nil {
		return ""
	}
	var kept []string
	for _, line := range lines(string(text)) {
		if !strings.HasPrefix(line, "#") {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// CommitMessage is the full message of a commit.
func (r *Repo) CommitMessage(id string) (string, error) {
	out, err := r.Git("log", "-1", "--format=%B", id)
	return strings.TrimRight(out, " \t\r\n"), err
}

func (r *Repo) LastCommitMessage() (string, error) {
	out, err := r.Git("log", "-1", "--format=%B")
	return strings.TrimRight(out, " \t\r\n"), err
}

func parseDecorations(decorations string) []RefLabel {
	var labels []RefLabel
	for _, d := range nonEmpty(decorations, ", ") {
		rest, isHead := strings.CutPrefix(d, "HEAD -> ")
		if isHead || d == "HEAD" {
			labels = append(labels, RefLabel{Name: "HEAD", Kind: RefHead})
			if d == "HEAD" {
				continue
			}
		}
		d = strings.TrimPrefix(rest, "tag: ")
		if name, ok := strings.CutPrefix(d, "refs/heads/"); ok {
			labels = append(labels, RefLabel{name, RefLocal})
		} else if name, ok := strings.CutPrefix(d, "refs/remotes/"); ok {
			if !strings.HasSuffix(name, "/HEAD") {
				labels = append(labels, RefLabel{name, RefRemote})
			}
		} else if name, ok := strings.CutPrefix(d, "refs/tags/"); ok {
			labels = append(labels, RefLabel{name, RefTag})
		}
	}
	return labels
}
