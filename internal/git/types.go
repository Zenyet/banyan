package git

import (
	"fmt"
	"strings"

	"github.com/Zenyet/banyan/internal/i18n"
)

type RefKind int

const (
	RefHead RefKind = iota
	RefLocal
	RefRemote
	RefTag
)

func (k RefKind) String() string {
	return [...]string{"Head", "Local", "Remote", "Tag"}[k]
}

type RefLabel struct {
	Name string
	Kind RefKind
}

type Commit struct {
	ID      string
	Parents []string
	Author  string
	Email   string
	Time    int64
	Subject string
	Refs    []RefLabel
}

func (c *Commit) ShortID() string { return Short(c.ID) }

// Short is the first eight characters of a commit id.
func Short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// LogFilter is what the log shows: git does the filtering, so it reaches
// past the commits already loaded.
type LogFilter struct {
	// Refs is a branch or other revision, or everything when empty.
	Refs string
	// Since is a --since date such as 1.week.ago.
	Since string
	// Authors are author names, any of which matches.
	Authors []string
	// Paths are files or folders relative to the repository root, any of
	// which matches.
	Paths []string
	// Text is text the commit message contains, ignoring case.
	Text string
}

func (f *LogFilter) Equal(o *LogFilter) bool {
	return f.Refs == o.Refs && f.Since == o.Since && f.Text == o.Text &&
		strings.Join(f.Authors, "\x00") == strings.Join(o.Authors, "\x00") &&
		strings.Join(f.Paths, "\x00") == strings.Join(o.Paths, "\x00")
}

func (f *LogFilter) IsEmpty() bool { return f.Equal(&LogFilter{}) }

// UpdateMethod is how Update Project brings in the tracked branch.
type UpdateMethod int

const (
	UpdateMerge UpdateMethod = iota
	UpdateRebase
	// UpdateFastForwardOnly updates only when the branch has no commits of
	// its own.
	UpdateFastForwardOnly
)

var AllUpdateMethods = []UpdateMethod{UpdateMerge, UpdateRebase, UpdateFastForwardOnly}

// Key is its name in the settings file.
func (m UpdateMethod) Key() string {
	return [...]string{"merge", "rebase", "ff-only"}[m]
}

func UpdateMethodFromKey(key string) UpdateMethod {
	for _, m := range AllUpdateMethods {
		if m.Key() == key {
			return m
		}
	}
	return UpdateMerge
}

func (m UpdateMethod) Label() string {
	switch m {
	case UpdateRebase:
		return i18n.T("Rebase onto incoming changes")
	case UpdateFastForwardOnly:
		return i18n.T("Fast-forward only")
	}
	return i18n.T("Merge incoming changes")
}

func (m UpdateMethod) Detail() string {
	switch m {
	case UpdateRebase:
		return i18n.T("Replays your local commits on top, keeping history linear")
	case UpdateFastForwardOnly:
		return i18n.T("Stops instead if your branch has commits of its own")
	}
	return i18n.T("Creates a merge commit if both sides changed")
}

// FastForward is fast-forwarding in a merge.
type FastForward int

const (
	// FastForwardAllow fast-forwards when possible, git's default.
	FastForwardAllow FastForward = iota
	// FastForwardOnly fails unless the merge is a fast-forward.
	FastForwardOnly
	// FastForwardNever always creates a merge commit.
	FastForwardNever
)

// MergeOptions are the options of a merge, as in JetBrains' Merge dialog.
type MergeOptions struct {
	FastForward FastForward
	// Squash puts the merged changes in the work tree and index as one
	// change to commit.
	Squash bool
	// NoCommit stops before committing the merge.
	NoCommit bool
	// Message is the merge commit's message instead of git's; empty for
	// git's.
	Message string
}

func (o *MergeOptions) Args() []string {
	args := []string{"--no-edit"}
	switch o.FastForward {
	case FastForwardOnly:
		args = append(args, "--ff-only")
	case FastForwardNever:
		args = append(args, "--no-ff")
	}
	if o.Squash {
		args = append(args, "--squash")
	}
	if o.NoCommit {
		args = append(args, "--no-commit")
	}
	if o.Message != "" {
		args = append(args, "-m", o.Message)
	}
	return args
}

// StashOptions is what git stash push puts aside.
type StashOptions struct {
	Message string
	// KeepIndex leaves staged changes in place as well as stashing them.
	KeepIndex        bool
	IncludeUntracked bool
	// Paths are the only files stashed, or everything when empty.
	Paths []string
}

// FileRevision is a commit that touched a file, with the file's path at
// that commit.
type FileRevision struct {
	Commit Commit
	Path   string
}

type BlameLine struct {
	ID      string
	Author  string
	Time    int64
	Summary string
	Text    string
	// Path is the file's path in that commit, which a rename may have
	// changed since.
	Path string
	// OrigLine is the line's number in that commit's version of the file,
	// from 1.
	OrigLine int
	// Previous is the commit before it that touched the file, and
	// PreviousPath the file's path there: what "Annotate Previous Revision"
	// shows. Empty where the line was added with the file.
	Previous, PreviousPath string
}

type CommitDetails struct {
	ID         string
	Parents    []string
	Author     string
	Email      string
	AuthorTime int64
	Committer  string
	CommitTime int64
	Message    string
	// Base is the parent the files changed against, parents[Base]; a merge
	// can be seen against any parent.
	Base      int
	Files     []FileChange
	Branches  []string
	Signature Signature
}

// BaseParent is the parent the files are compared with, "" for a root
// commit.
func (d *CommitDetails) BaseParent() string {
	if d.Base < len(d.Parents) {
		return d.Parents[d.Base]
	}
	return ""
}

type SignatureKind int

const (
	Unsigned SignatureKind = iota
	// SignatureGood is a good signature by Signer.
	SignatureGood
	// SignatureUntrusted is a good signature by a key that isn't trusted.
	SignatureUntrusted
	SignatureBad
	// SignatureExpired is an expired or revoked signature or key.
	SignatureExpired
	// SignatureUnchecked couldn't be checked, e.g. the key is unknown.
	SignatureUnchecked
)

// Signature is whether a commit is signed, and how its signature checks
// out.
type Signature struct {
	Kind   SignatureKind
	Signer string
}

func parseSignature(code, signer string) Signature {
	switch strings.TrimSpace(code) {
	case "G":
		return Signature{SignatureGood, strings.TrimSpace(signer)}
	case "U":
		return Signature{SignatureUntrusted, strings.TrimSpace(signer)}
	case "B":
		return Signature{Kind: SignatureBad}
	case "X", "Y", "R":
		return Signature{Kind: SignatureExpired}
	case "E":
		return Signature{Kind: SignatureUnchecked}
	}
	return Signature{}
}

// CommitOptions are choices for one commit beyond its message and files,
// like JetBrains' Commit Options.
type CommitOptions struct {
	// Author is "Name <email>" to commit as another author.
	Author string
	// SignOff adds a Signed-off-by line.
	SignOff bool
	// Sign signs the commit; nil leaves it to commit.gpgsign.
	Sign *bool
	// SkipHooks skips the pre-commit and commit-msg hooks.
	SkipHooks bool
}

func (o *CommitOptions) args() []string {
	var args []string
	if o.Author != "" {
		args = append(args, "--author="+o.Author)
	}
	if o.SignOff {
		args = append(args, "--signoff")
	}
	if o.Sign != nil {
		if *o.Sign {
			args = append(args, "--gpg-sign")
		} else {
			args = append(args, "--no-gpg-sign")
		}
	}
	if o.SkipHooks {
		args = append(args, "--no-verify")
	}
	return args
}

// IsValidAuthor reports whether author reads "Name <email>", as git commit
// --author wants it.
func IsValidAuthor(author string) bool {
	name, rest, ok := strings.Cut(strings.TrimSpace(author), "<")
	return ok && strings.TrimSpace(name) != "" && strings.HasSuffix(rest, ">") && len(rest) > 1
}

// SigningConfig is how the repository signs commits.
type SigningConfig struct {
	// Always is commit.gpgsign: every commit is signed.
	Always bool
	// Key is user.signingkey, if set.
	Key string
	// Format is gpg.format: openpgp (the default), ssh or x509.
	Format string
}

type FileChange struct {
	// Status is A, M, D, R, C or T.
	Status   byte
	Path     string
	OrigPath string
}

type Branch struct {
	// Name is the short name, e.g. main or origin/main.
	Name     string
	Kind     RefKind
	Target   string
	Upstream string
	// Track is e.g. "ahead 1, behind 2".
	Track  string
	IsHead bool
}

// FullName is e.g. refs/heads/main.
func (b *Branch) FullName() string {
	switch b.Kind {
	case RefRemote:
		return "refs/remotes/" + b.Name
	case RefTag:
		return "refs/tags/" + b.Name
	}
	return "refs/heads/" + b.Name
}

type StatusEntry struct {
	Path     string
	OrigPath string
	Index    byte
	Worktree byte
}

func (e *StatusEntry) IsUntracked() bool { return e.Index == '?' }

func (e *StatusEntry) IsConflicted() bool {
	return e.Index == 'U' || e.Worktree == 'U' || (e.Index == 'A' && e.Worktree == 'A') || (e.Index == 'D' && e.Worktree == 'D')
}

// DisplayStatus is one status letter as JetBrains shows it: the combined
// state against HEAD.
func (e *StatusEntry) DisplayStatus() byte {
	switch {
	case e.IsUntracked():
		return '?'
	case e.IsConflicted():
		return 'U'
	case e.Index == 'A':
		return 'A'
	case e.Index == 'D' || e.Worktree == 'D':
		return 'D'
	case e.Index == 'R':
		return 'R'
	}
	return 'M'
}

// ConflictSides is what each side of a conflict did to the file: yours and
// theirs.
func (e *StatusEntry) ConflictSides() (ours, theirs ConflictSide, ok bool) {
	switch string([]byte{e.Index, e.Worktree}) {
	case "UU":
		return SideModified, SideModified, true
	case "AA":
		return SideAdded, SideAdded, true
	case "DU":
		return SideDeleted, SideModified, true
	case "UD":
		return SideModified, SideDeleted, true
	case "AU":
		return SideAdded, SideAbsent, true
	case "UA":
		return SideAbsent, SideAdded, true
	case "DD":
		return SideDeleted, SideDeleted, true
	}
	return 0, 0, false
}

// CanMergeText reports whether both sides have content to merge line by
// line.
func (e *StatusEntry) CanMergeText() bool {
	ours, theirs, ok := e.ConflictSides()
	return ok && ours.hasContent() && theirs.hasContent()
}

type ConflictSide int

const (
	SideModified ConflictSide = iota
	SideAdded
	SideDeleted
	// SideAbsent never had the file.
	SideAbsent
)

func (s ConflictSide) Label() string {
	return [...]string{"Modified", "Added", "Deleted", "—"}[s]
}

func (s ConflictSide) hasContent() bool { return s == SideModified || s == SideAdded }

type RepoState int

const (
	Clean RepoState = iota
	Merging
	Rebasing
	CherryPicking
	Reverting
)

// Noun is the operation's name for buttons, e.g. "Abort Merge".
func (s RepoState) Noun() string {
	switch s {
	case Merging:
		return i18n.T("Merge")
	case Rebasing:
		return i18n.T("Rebase")
	case CherryPicking:
		return i18n.T("Cherry-Pick")
	case Reverting:
		return i18n.T("Revert")
	}
	return ""
}

// ContinueLabel is the label of the button that finishes the operation.
func (s RepoState) ContinueLabel() string {
	if s == Merging {
		return i18n.T("Commit Merge")
	}
	return i18n.Tf("Continue {}", s.Noun())
}

// Operation is a merge, rebase, cherry-pick or revert that stopped, as the
// operation banner and the merge tool describe it.
type Operation struct {
	State RepoState
	// Title is e.g. "Merging feature into main".
	Title string
	// Ours and Theirs name git's sides of a conflict.
	Ours, Theirs string
}

// MarkerSize is the size of conflict markers, longer than git's usual
// seven, so that markers already in a file's content are not mistaken for
// ours.
const MarkerSize = 12

type ResetMode int

const (
	ResetSoft ResetMode = iota
	ResetMixed
	ResetHard
)

type RebaseActionKind int

const (
	Pick RebaseActionKind = iota
	// Reword picks with a new message.
	Reword
	Squash
	Fixup
	Drop
	// Edit picks, then stops so the commit can be amended or split.
	Edit
)

type RebaseAction struct {
	Kind RebaseActionKind
	// Message is the new message of a Reword.
	Message string
}

// RebaseStep is one commit of an interactive rebase's plan.
type RebaseStep struct {
	ID     string
	Action RebaseAction
}

// Worktree is a working tree of the repository, from git worktree list.
type Worktree struct {
	Path string
	Head string
	// Branch is the branch checked out there; empty when detached.
	Branch string
	// IsMain is the repository's first working tree, which cannot be
	// removed.
	IsMain bool
	// IsCurrent is the working tree this window shows.
	IsCurrent bool
	Locked    bool
	// Prunable is a working tree whose folder is gone; pruning forgets it.
	Prunable bool
}

type SubmoduleState int

const (
	// SubmoduleUninitialized is not cloned yet.
	SubmoduleUninitialized SubmoduleState = iota
	SubmoduleUpToDate
	// SubmoduleOutOfDate is checked out at another commit than the one
	// recorded.
	SubmoduleOutOfDate
	SubmoduleConflict
)

// Submodule is a submodule, from git submodule status.
type Submodule struct {
	Path string
	// Commit is the commit checked out, or recorded when not initialized.
	Commit string
	State  SubmoduleState
}

type RewriteKind int

const (
	RewriteReword RewriteKind = iota
	// RewriteSquash melds IDs into the oldest of them, with Message.
	RewriteSquash
	// RewriteFixupIntoParent melds a commit into its parent, keeping the
	// parent's message.
	RewriteFixupIntoParent
	RewriteDrop
)

// Rewrite is a change to the current branch's history, as the Log's commit
// menu offers.
type Rewrite struct {
	Kind    RewriteKind
	IDs     []string
	Message string
}

type Remote struct {
	Name     string
	FetchURL string
	PushURL  string
}

type PushTarget struct {
	Branch       string
	Remote       string
	RemoteBranch string
	// IsNew means the remote branch does not exist yet; the push sets it as
	// upstream.
	IsNew bool
}

type Stash struct {
	// Name is e.g. stash@{0}.
	Name    string
	Message string
	// Commit is the stash commit; its first parent is the commit it was
	// made on.
	Commit Commit
}

// Error is a failed git command.
type Error struct {
	Args    []string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), e.Message)
}
