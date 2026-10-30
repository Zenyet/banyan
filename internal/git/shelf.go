package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Zenyet/banyan/internal/changelists"
)

// Shelf is changes put aside with Shelve, kept as a patch in
// banyan/shelf/<id> of the git directory, like JetBrains' shelf.
type Shelf struct {
	ID   string
	Name string
	// Changelist is the changelist the changes came from, which unshelving
	// restores.
	Changelist string
	Time       int64
	Files      []string
}

func readShelf(dir string) (Shelf, bool) {
	info, err := os.ReadFile(filepath.Join(dir, "info"))
	if err != nil {
		return Shelf{}, false
	}
	s := Shelf{ID: filepath.Base(dir), Changelist: changelists.Default}
	for _, line := range lines(string(info)) {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "name":
			s.Name = value
		case "changelist":
			s.Changelist = value
		case "time":
			s.Time, _ = strconv.ParseInt(value, 10, 64)
		case "file":
			s.Files = append(s.Files, value)
		}
	}
	return s, true
}

func (s *Shelf) write(dir string) error {
	oneLine := strings.NewReplacer("\n", " ", "\r", " ").Replace
	var b strings.Builder
	fmt.Fprintf(&b, "name=%s\nchangelist=%s\ntime=%d\n", oneLine(s.Name), oneLine(s.Changelist), s.Time)
	for _, f := range s.Files {
		fmt.Fprintf(&b, "file=%s\n", f)
	}
	return os.WriteFile(filepath.Join(dir, "info"), []byte(b.String()), 0o644)
}

// patchBlocks returns the per-file parts of a patch, each starting at its
// diff --git line.
func patchBlocks(patch string) []string {
	var starts []int
	for ix := 0; ; {
		at := strings.Index(patch[ix:], "diff --git ")
		if at < 0 {
			break
		}
		at += ix
		if at == 0 || patch[at-1] == '\n' {
			starts = append(starts, at)
		}
		ix = at + 1
	}
	starts = append(starts, len(patch))
	var blocks []string
	for i := 0; i+1 < len(starts); i++ {
		blocks = append(blocks, patch[starts[i]:starts[i+1]])
	}
	return blocks
}

// blockIsFor reports whether a patch block is the one for path (its new
// name).
func blockIsFor(block, path string) bool {
	header, _, _ := strings.Cut(block, "\n")
	return strings.HasSuffix(header, " b/"+path)
}

// globLiteral is path as a literal pattern for git apply --include.
func globLiteral(path string) string {
	var b strings.Builder
	for _, c := range path {
		if strings.ContainsRune(`*?[]\`, c) {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func (r *Repo) shelfRoot() string { return filepath.Join(r.gitDir, "banyan", "shelf") }

// Shelves returns shelved changes, newest first.
func (r *Repo) Shelves() []Shelf {
	entries, err := os.ReadDir(r.shelfRoot())
	if err != nil {
		return nil
	}
	var shelves []Shelf
	for _, e := range entries {
		if s, ok := readShelf(filepath.Join(r.shelfRoot(), e.Name())); ok {
			shelves = append(shelves, s)
		}
	}
	sort.Slice(shelves, func(i, j int) bool {
		if shelves[i].Time != shelves[j].Time {
			return shelves[i].Time > shelves[j].Time
		}
		return shelves[i].ID > shelves[j].ID
	})
	return shelves
}

// Shelve puts the changes of paths aside as shelf name and takes them out
// of the work tree, new files included. changelist is where unshelving puts
// them back.
func (r *Repo) Shelve(name, changelist string, paths []string) (*Shelf, error) {
	if !r.HasHead() {
		return nil, errors.New("shelving compares with the last commit; commit something first")
	}
	if r.State() != Clean {
		return nil, errors.New("finish or abort the current merge, rebase or cherry-pick first")
	}
	status, err := r.Status()
	if err != nil {
		return nil, err
	}
	var entries []StatusEntry
	for _, e := range status {
		if slices.Contains(paths, e.Path) {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return nil, errors.New("no changes to shelve")
	}
	var untracked, pathspec []string
	for _, e := range entries {
		if e.IsConflicted() {
			return nil, errors.New("resolve the conflicts before shelving")
		}
		if e.IsUntracked() {
			untracked = append(untracked, e.Path)
		}
		pathspec = append(pathspec, e.Path)
	}
	for _, e := range entries {
		if e.OrigPath != "" {
			pathspec = append(pathspec, e.OrigPath)
		}
	}
	undoIntent := func() {
		if len(untracked) > 0 {
			r.Git(append([]string{"rm", "-q", "--cached", "--ignore-unmatch", "--"}, untracked...)...)
		}
	}
	if len(untracked) > 0 {
		// Intent-to-add makes new files part of the diff.
		if _, err := r.Git(append([]string{"add", "-N", "--"}, untracked...)...); err != nil {
			return nil, err
		}
	}
	patch, err := r.Git(append([]string{"diff", "HEAD", "--binary", "--full-index", "-M", "--"}, pathspec...)...)
	if err != nil {
		undoIntent()
		return nil, err
	}
	if strings.TrimSpace(patch) == "" {
		undoIntent()
		return nil, errors.New("no changes to shelve")
	}

	now := time.Now()
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		files = append(files, e.Path)
	}
	sort.Strings(files)
	shelf := &Shelf{
		ID:         fmt.Sprintf("%d-%09d", now.Unix(), now.Nanosecond()),
		Name:       strings.TrimSpace(name),
		Changelist: changelist,
		Time:       now.Unix(),
		Files:      files,
	}
	dir := filepath.Join(r.shelfRoot(), shelf.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	patchFile := filepath.Join(dir, "patch.diff")
	if err := os.WriteFile(patchFile, []byte(patch), 0o644); err != nil {
		return nil, err
	}
	if err := shelf.write(dir); err != nil {
		return nil, err
	}
	// Only touch the work tree once the patch is known to undo cleanly.
	if _, err := r.Git("apply", "--check", "-R", "--binary", patchFile); err != nil {
		os.RemoveAll(dir)
		undoIntent()
		return nil, fmt.Errorf("the shelf patch does not undo cleanly; nothing was changed: %w", err)
	}

	tree, err := r.Git(append([]string{"ls-tree", "-r", "-z", "--name-only", "HEAD", "--"}, pathspec...)...)
	if err != nil {
		return nil, err
	}
	inHead := nonEmpty(tree, "\x00")
	var restore, remove []string
	for _, p := range pathspec {
		if slices.Contains(inHead, p) {
			restore = append(restore, p)
		} else {
			remove = append(remove, p)
		}
	}
	if len(restore) > 0 {
		if _, err := r.Git(append([]string{"checkout", "HEAD", "--"}, restore...)...); err != nil {
			return nil, err
		}
	}
	if len(remove) > 0 {
		if _, err := r.Git(append([]string{"rm", "-q", "--cached", "-f", "--ignore-unmatch", "--"}, remove...)...); err != nil {
			return nil, err
		}
		for _, p := range remove {
			os.Remove(filepath.Join(r.root, p))
		}
	}
	return shelf, nil
}

// Unshelve applies shelved changes again: all of shelf id, or only paths of
// it (nil for all). Applied files leave the shelf and an emptied shelf goes.
// It returns the shelf as it was and the files restored. Hunks that no
// longer fit are merged three-way and left with conflict markers.
func (r *Repo) Unshelve(id string, paths []string) (*Shelf, []string, error) {
	dir := filepath.Join(r.shelfRoot(), id)
	shelf, ok := readShelf(dir)
	if !ok {
		return nil, nil, errors.New("this shelf no longer exists")
	}
	files := shelf.Files
	if paths != nil {
		files = nil
		for _, f := range shelf.Files {
			if slices.Contains(paths, f) {
				files = append(files, f)
			}
		}
	}
	if len(files) == 0 {
		return nil, nil, errors.New("nothing to unshelve")
	}
	patchFile := filepath.Join(dir, "patch.diff")
	patchBytes, err := os.ReadFile(patchFile)
	if err != nil {
		return nil, nil, err
	}
	patch := string(patchBytes)
	args := []string{"apply", "--binary"}
	for _, f := range files {
		args = append(args, "--include="+globLiteral(f))
	}
	args = append(args, patchFile)
	if _, err := r.Git(args...); err != nil {
		threeWay := append([]string{"apply", "--3way"}, args[1:]...)
		// Applied, leaving conflicts to resolve, is fine.
		if _, err := r.Git(threeWay...); err != nil && !strings.Contains(err.Error(), "with conflicts") {
			return nil, nil, err
		}
	}
	// Files the patch adds show up as new files, not unversioned ones.
	var added []string
	for _, block := range patchBlocks(patch) {
		if !strings.Contains(block, "\nnew file mode") {
			continue
		}
		for _, f := range files {
			if blockIsFor(block, f) && exists(filepath.Join(r.root, f)) {
				added = append(added, f)
				break
			}
		}
	}
	if len(added) > 0 {
		r.Git(append([]string{"add", "-N", "--"}, added...)...)
	}

	var remaining []string
	for _, f := range shelf.Files {
		if !slices.Contains(files, f) {
			remaining = append(remaining, f)
		}
	}
	if len(remaining) == 0 {
		if err := os.RemoveAll(dir); err != nil {
			return nil, nil, err
		}
	} else {
		var kept strings.Builder
		for _, block := range patchBlocks(patch) {
			if slices.ContainsFunc(remaining, func(f string) bool { return blockIsFor(block, f) }) {
				kept.WriteString(block)
			}
		}
		if err := os.WriteFile(patchFile, []byte(kept.String()), 0o644); err != nil {
			return nil, nil, err
		}
		rest := shelf
		rest.Files = remaining
		if err := rest.write(dir); err != nil {
			return nil, nil, err
		}
	}
	return &shelf, files, nil
}

// ShelfFilePatch is the part of shelf id's patch for path.
func (r *Repo) ShelfFilePatch(id, path string) (string, error) {
	patch, err := os.ReadFile(filepath.Join(r.shelfRoot(), id, "patch.diff"))
	if err != nil {
		return "", err
	}
	for _, block := range patchBlocks(string(patch)) {
		if blockIsFor(block, path) {
			return block, nil
		}
	}
	return "", nil
}

func (r *Repo) RenameShelf(id, name string) error {
	dir := filepath.Join(r.shelfRoot(), id)
	shelf, ok := readShelf(dir)
	if !ok {
		return errors.New("this shelf no longer exists")
	}
	shelf.Name = strings.TrimSpace(name)
	return shelf.write(dir)
}

func (r *Repo) DeleteShelf(id string) error {
	if err := os.RemoveAll(filepath.Join(r.shelfRoot(), id)); err != nil {
		return fmt.Errorf("cannot delete the shelf: %w", err)
	}
	return nil
}
