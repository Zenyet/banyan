package model

import (
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

// gitEntries are the files and directories under .git whose changes matter
// to the views; changes elsewhere there (objects, logs) are left out.
var gitEntries = []string{
	"HEAD", "index", "refs", "packed-refs", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply",
}

// gitInternals are directories of the git directory never watched.
var gitInternals = []string{"objects", "logs", "lfs", "modules", "worktrees", "banyan", "banyan-rebase", "hooks"}

// changes are the paths changed since the last look, split by where they
// live.
type changes struct {
	// git is refs, HEAD, the index or an operation in progress.
	git      bool
	worktree []string
}

// watch refreshes when files, the index or refs change outside the app,
// like JetBrains' VFS refresh.
//
// The watches only hint at the directories something changed in, which
// costs nothing on big repositories: a change in the git directory's top
// level or its refs reads the repository again, one in the work tree only
// the status, unless git ignores where it was.
func (m *Model) watch() {
	root, gitDir := m.Repo.Root(), m.Repo.GitDir()
	found := make(chan changes, 16)
	// ignoring leaves out the git directory's objects, logs and the like,
	// under prefix in the watched directory.
	ignoring := func(prefix string) func(string, bool) bool {
		return func(p string, isDir bool) bool {
			rest, ok := strings.CutPrefix(p, prefix)
			return isDir && ok && slices.Contains(gitInternals, rest)
		}
	}
	start := func(dir string, isGitDir bool) {
		prefix := ".git/"
		if isGitDir {
			prefix = ""
		}
		w, err := mygo.WatchFiles(m.ctx, dir, mygo.FileWatchOptions{Recursive: true, HintsOnly: true, Ignore: ignoring(prefix)})
		if err != nil {
			return
		}
		go func() {
			defer w.Close()
			for {
				events, err := w.Next(m.ctx)
				if err != nil {
					return
				}
				var c changes
				for _, e := range events {
					rel := path.Clean(e.Path)
					if isGitDir {
						c.git = c.git || isRelevantGitPath(rel)
						continue
					}
					if gitRel, ok := strings.CutPrefix(rel, ".git"); ok && (gitRel == "" || gitRel[0] == '/') {
						c.git = c.git || isRelevantGitPath(strings.TrimPrefix(gitRel, "/"))
						continue
					}
					c.worktree = append(c.worktree, filepath.Join(root, filepath.FromSlash(rel)))
				}
				if c.git || len(c.worktree) > 0 {
					select {
					case found <- c:
					case <-m.ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() {
		start(root, false)
		// A worktree or submodule may keep its git directory elsewhere.
		if rel, err := filepath.Rel(root, gitDir); err != nil || strings.HasPrefix(rel, "..") {
			start(gitDir, true)
		}
		for {
			var c changes
			select {
			case <-m.ctx.Done():
				return
			case c = <-found:
			}
			// Gather what else comes in a moment.
			timer := time.After(300 * time.Millisecond)
		gather:
			for {
				select {
				case more := <-found:
					c.git = c.git || more.git
					c.worktree = append(c.worktree, more.worktree...)
				case <-timer:
					break gather
				}
			}
			relevant := c.git
			if !relevant {
				kept, err := m.Repo.NotIgnored(c.worktree)
				relevant = err == nil && len(kept) > 0
			}
			if relevant {
				git := c.git
				Post(func() {
					// An operation of our own refreshes when it finishes.
					if m.Busy == "" && m.ctx.Err() == nil {
						if git {
							m.Refresh()
						} else {
							m.refreshStatus()
						}
					}
				})
			}
		}
	}()
}

// isRelevantGitPath reports whether a change at rel, relative to the git
// directory, matters to the views. A hint names a directory: the git
// directory itself ("." or "") holds HEAD, the index and the operation
// files.
func isRelevantGitPath(rel string) bool {
	if rel == "" || rel == "." {
		return true
	}
	if strings.HasSuffix(rel, ".lock") {
		return false
	}
	first, _, _ := strings.Cut(rel, "/")
	for _, e := range gitEntries {
		if first == e {
			return true
		}
	}
	return false
}
