package app

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/settings"
)

// fileRow is a changed file in a list, in the color of its status.
func fileRow(c *ui.Context, f *git.FileChange, selected bool) ui.Element {
	t := c.Theme()
	color := statusColor(f.Status, t)
	row := ui.Row(c.Key(f.Path)).Height(22).PaddingX(8).Gap(4).Cursor(ui.CursorPointer)
	switch {
	case selected:
		row.Background(selectionBG)
	case row.Hovered():
		row.Background(hoverBG(t))
	}
	row.Children(func() {
		ui.Icon(c, icon("file")).FontSize(t.FontSize - 1).TextColor(color)
		ui.Text(c, f.Path).SingleLine().TextColor(color).Shrink(1)
	})
	return row
}

// commitBrowser is a list of commits with the chosen commit's files and
// diff, used by the branch comparison and stash dialogs.
type commitBrowser struct {
	ws           *Workspace
	commits      []git.Commit
	selected     int
	details      *git.CommitDetails
	file         int
	diff         *DiffPane
	emptyMessage string
	gen          int
	width        float32
	filesHeight  float32
}

func newCommitBrowser(ws *Workspace) *commitBrowser {
	b := &commitBrowser{ws: ws, diff: newDiffPane(), emptyMessage: i18n.T("No commits"), selected: -1, width: 380, filesHeight: 150}
	b.diff.Clear()
	b.diff.Remote = ws.Model.Repo.IsPartialClone()
	return b
}

func (b *commitBrowser) setCommits(commits []git.Commit, empty string) {
	b.commits, b.emptyMessage = commits, empty
	if len(commits) > 0 {
		b.selectCommit(0)
	} else {
		b.selectCommit(-1)
	}
}

func (b *commitBrowser) selectCommit(ix int) {
	b.selected, b.details, b.file = ix, nil, -1
	b.gen++
	if ix < 0 || ix >= len(b.commits) {
		b.diff.ShowMessage("", b.emptyMessage)
		return
	}
	gen, id, repo := b.gen, b.commits[ix].ID, b.ws.Model.Repo
	go func() {
		d, err := repo.CommitDetails(id)
		model.Post(func() {
			if gen != b.gen {
				return
			}
			switch {
			case err != nil:
				b.diff.ShowMessage("", err.Error())
			case len(d.Files) == 0:
				b.details = d
				b.diff.ShowMessage("", i18n.T("No changed files"))
			default:
				b.details = d
				b.selectFile(0)
			}
		})
	}()
}

func (b *commitBrowser) selectFile(ix int) {
	d := b.details
	if d == nil || ix >= len(d.Files) {
		return
	}
	b.file = ix
	f, repo := d.Files[ix], b.ws.Model.Repo
	b.diff.Load(f.Path, func() (string, error) { return repo.CommitFileDiff(d, &f) })
}

func (b *commitBrowser) View(c *ui.Context) {
	t := c.Theme()
	ui.Split(c.Key("browser"), &b.width, func() {
		if len(b.commits) == 0 {
			emptyMessage(c, b.emptyMessage)
			return
		}
		ui.Scroll(c.Key("commits")).Fill().Children(func() {
			for ix, commit := range b.commits {
				row := ui.Row(c.Key(ix)).Height(24).PaddingX(8).Gap(8).Cursor(ui.CursorPointer)
				switch {
				case ix == b.selected:
					row.Background(selectionBG)
				case row.Hovered():
					row.Background(hoverBG(t))
				}
				if row.Clicked() {
					b.selectCommit(ix)
				}
				row.Children(func() {
					ui.Text(c, commit.Subject).SingleLine().Grow(1).Shrink(1)
					muted(c, commit.Author+"  "+formatTime(commit.Time)).SingleLine()
				})
			}
		})
	}, func() {
		ui.SplitVertical(c.Key("right"), &b.filesHeight, func() {
			ui.Scroll(c.Key("files")).Fill().PaddingY(4).Children(func() {
				if b.details == nil {
					return
				}
				for ix := range b.details.Files {
					if fileRow(c, &b.details.Files[ix], ix == b.file).Clicked() {
						b.selectFile(ix)
					}
				}
			})
		}, func() {
			ui.Column(c).Fill().Children(func() { b.diff.View(c) })
		}).Fill()
	}).Fill()
}

// framed is a bordered box of a fixed height in a dialog.
func framed(c *ui.Context, height float32, fn func()) {
	t := c.Theme()
	ui.Column(c.Key("frame")).Height(height).FillWidth().Border(1, t.Border).Radius(t.Radius).Clip().Children(fn)
}

// openPush is the JetBrains-style Push dialog: review outgoing commits and
// their files before pushing.
func openPush(ws *Workspace) {
	var target *git.PushTarget
	var commits []git.Commit
	var files []git.FileChange
	var loadErr error
	loading := true
	selected := -1
	force, tags := false, false
	width := float32(520)
	gen := 0
	choose := func(ix int) {
		selected = ix
		gen++
		g, id, repo := gen, commits[ix].ID, ws.Model.Repo
		go func() {
			d, err := repo.CommitDetails(id)
			model.Post(func() {
				if g == gen {
					files = nil
					if err == nil {
						files = d.Files
					}
				}
			})
		}()
	}
	repo := ws.Model.Repo
	go func() {
		tg, err := repo.PushTarget()
		var cs []git.Commit
		if err == nil {
			cs, err = repo.Outgoing(tg)
		}
		model.Post(func() {
			loading, target, commits, loadErr = false, tg, cs, err
			if len(commits) > 0 {
				choose(0)
			}
		})
	}()
	ws.Shell.Open(&Dialog{Title: i18n.T("Push Commits"), Width: 900, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		switch {
		case loadErr != nil:
			ui.Text(c, loadErr.Error()).TextColor(t.Danger)
		case target != nil:
			ui.Row(c).Gap(6).Children(func() {
				ui.Icon(c, icon("git-branch"))
				ui.Text(c, target.Branch)
				muted(c, "→")
				ui.Text(c, target.Remote+" : "+target.RemoteBranch)
				if target.IsNew {
					ui.Box(c).PaddingX(4).Radius(3).Background(ui.Hex("#5f9e5f")).Children(func() {
						ui.Text(c, i18n.T("New")).FontSize(t.FontSize - 2).TextColor(ui.RGB(255, 255, 255))
					})
				}
			})
		default:
			muted(c, i18n.T("Loading…"))
		}
		framed(c, 380, func() {
			if !loading && len(commits) == 0 && loadErr == nil {
				emptyMessage(c, i18n.T("Nothing to push"))
				return
			}
			ui.Split(c.Key("push"), &width, func() {
				ui.Scroll(c.Key("commits")).Fill().Children(func() {
					for ix, commit := range commits {
						row := ui.Row(c.Key(ix)).Height(24).PaddingX(8).Gap(8).Cursor(ui.CursorPointer)
						switch {
						case ix == selected:
							row.Background(selectionBG)
						case row.Hovered():
							row.Background(hoverBG(t))
						}
						if row.Clicked() {
							choose(ix)
						}
						row.Children(func() {
							ui.Text(c, commit.Subject).SingleLine().Grow(1).Shrink(1)
							muted(c, formatTime(commit.Time)).SingleLine()
						})
					}
				})
			}, func() {
				ui.Scroll(c.Key("files")).Fill().PaddingY(4).Children(func() {
					for ix := range files {
						fileRow(c, &files[ix], false)
					}
				})
			}).Fill()
		})
		protected := target != nil && settings.Read(func(s *settings.Settings) bool { return s.IsProtected(target.RemoteBranch) })
		canPush := target != nil && (len(commits) > 0 || force || tags)
		ui.Row(c).Gap(8).Children(func() {
			if protected {
				muted(c, i18n.T("Protected branch: no force push"))
				force = false
			} else {
				ui.Checkbox(c.Key("force"), &force, i18n.T("Force push (--force-with-lease)"))
			}
			ui.Checkbox(c.Key("tags"), &tags, i18n.T("Push tags"))
			ui.Spacer(c)
			if ui.Button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
				d.Close()
			}
			label := i18n.T("Push")
			if force {
				label = i18n.T("Force Push")
			}
			ok := ui.PrimaryButton(c.Key("push"), label).Disabled(!canPush)
			if force {
				ok.Background(t.Danger)
			}
			if ok.Clicked() {
				tg, f, tt := *target, force, tags
				d.Close()
				ws.run(label, func(r *git.Repo) (string, error) { return r.PushTo(&tg, f, tt) })
			}
		})
	}})
}

// openCompare is "Compare with Current" and "Compare with…": commits a
// branch has that HEAD (or base) lacks, and the other way round.
func openCompare(ws *Workspace, branch, base string) {
	title := i18n.Tf("Compare {} with Current Branch", branch)
	current := ws.Model.CurrentBranch
	if current == "" {
		current = "HEAD"
	}
	if base != "" {
		title = i18n.Tf("Compare {} with {}", branch, base)
		current = base
	} else {
		base = "HEAD"
	}
	b := newCommitBrowser(ws)
	var incoming, outgoing []git.Commit
	loading, showingIncoming := true, true
	show := func(in bool) {
		showingIncoming = in
		if in {
			b.setCommits(incoming, i18n.Tf("{} has no commits that {} lacks", branch, current))
		} else {
			b.setCommits(outgoing, i18n.Tf("{} has no commits that {} lacks", current, branch))
		}
	}
	repo := ws.Model.Repo
	go func() {
		in, out, err := repo.CompareRefs(branch, base)
		model.Post(func() {
			loading = false
			if err != nil {
				b.setCommits(nil, err.Error())
				return
			}
			incoming, outgoing = in, out
			show(true)
		})
	}()
	ws.Shell.Open(&Dialog{Title: title, Width: 1200, Body: func(c *ui.Context, d *Dialog) {
		ui.Row(c).Gap(8).Children(func() {
			if toggleButton(c.Key("in"), i18n.Tf("In {} but not in {} ({})", branch, current, len(incoming)), showingIncoming).Clicked() {
				show(true)
			}
			if toggleButton(c.Key("out"), i18n.Tf("In {} but not in {} ({})", current, branch, len(outgoing)), !showingIncoming).Clicked() {
				show(false)
			}
			ui.Spacer(c)
			if loading {
				muted(c, i18n.T("Loading…"))
			}
		})
		framed(c, 600, func() { b.View(c) })
	}})
}

// openStashViewer browses stashes with their files and diffs, and pops,
// applies or drops them.
func openStashViewer(ws *Workspace) {
	b := newCommitBrowser(ws)
	var stashes []git.Stash
	var shown uint64 = ^uint64(0)
	version := func() uint64 {
		var v uint64
		for _, s := range ws.Model.Stashes {
			for _, ch := range s.Commit.ID {
				v = v*31 + uint64(ch)
			}
		}
		return v
	}
	ws.Shell.Open(&Dialog{Title: i18n.T("Stashes"), Width: 1200, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		// Popping or dropping changes the list.
		if v := version(); v != shown {
			shown = v
			stashes = slices.Clone(ws.Model.Stashes)
			var commits []git.Commit
			for _, s := range stashes {
				commit := s.Commit
				commit.Subject = s.Name + "  " + s.Message
				commits = append(commits, commit)
			}
			b.setCommits(commits, i18n.T("No stashes"))
		}
		framed(c, 600, func() { b.View(c) })
		has := b.selected >= 0 && b.selected < len(stashes)
		name := ""
		if has {
			name = stashes[b.selected].Name
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c.Key("pop"), i18n.T("Pop")).Disabled(!has).Clicked() {
				ws.run(i18n.T("Pop")+" "+name, func(r *git.Repo) (string, error) { return r.StashPop(name) })
			}
			if ui.Button(c.Key("apply"), i18n.T("Apply")).Disabled(!has).Clicked() {
				ws.run(i18n.T("Apply")+" "+name, func(r *git.Repo) (string, error) { return r.StashApply(name) })
			}
			if ui.Button(c.Key("drop"), i18n.T("Drop")).TextColor(t.Danger).Disabled(!has).Clicked() {
				ws.Shell.Confirm(i18n.T("Drop Stash"), i18n.T("Drop this stash? This cannot be undone."), i18n.T("Drop"), func() {
					ws.run(i18n.T("Drop")+" "+name, func(r *git.Repo) (string, error) { return r.StashDrop(name) })
				})
			}
			br := ui.Button(c.Key("branch"), i18n.T("Unstash as Branch…")).Disabled(!has).
				Tooltip(i18n.T("Check out a new branch where the stash was made and apply it there"))
			if br.Clicked() {
				d.Close()
				ws.Shell.PromptText(i18n.T("Unstash as Branch"), i18n.T("Create Branch"), "", func(branch string) {
					ws.run(i18n.Tf("Unstash {} as {}", name, branch), func(r *git.Repo) (string, error) { return r.StashBranch(name, branch) })
				})
			}
			ui.Spacer(c)
			if ui.Button(c.Key("close"), i18n.T("Close")).Clicked() {
				d.Close()
			}
		})
	}})
}

// openWorktreeDiff shows the changes from from to to, or to the work tree
// when to is empty; only those of path when given: "Show Diff with Working
// Tree", "Compare with Local" and comparing two commits of the Log.
func openWorktreeDiff(ws *Workspace, from, to, path string) {
	target := i18n.T("Working Tree")
	if to != "" {
		target = short(to)
	}
	title := i18n.Tf("Diff between {} and {}", short(from), target)
	if path != "" {
		title = path + ": " + short(from) + " vs " + target
	}
	var files []git.FileChange
	loading, selected := true, -1
	diff := newDiffPane()
	diff.ShowMessage("", i18n.T("Loading…"))
	width := float32(320)
	repo := ws.Model.Repo
	choose := func(ix int) {
		selected = ix
		f := files[ix]
		diff.Load(f.Path, func() (string, error) { return repo.DiffBetween(from, to, &f) })
	}
	go func() {
		all, err := repo.ChangesBetween(from, to)
		model.Post(func() {
			loading = false
			if err != nil {
				diff.ShowMessage("", err.Error())
				return
			}
			for _, f := range all {
				if path == "" || f.Path == path || f.OrigPath == path {
					files = append(files, f)
				}
			}
			if len(files) == 0 {
				diff.ShowMessage("", i18n.T("No differences"))
			} else {
				choose(0)
			}
		})
	}()
	ws.Shell.Open(&Dialog{Title: title, Width: 1200, Body: func(c *ui.Context, d *Dialog) {
		framed(c, 620, func() {
			ui.Split(c.Key("worktree-diff"), &width, func() {
				ui.Column(c).Fill().Children(func() {
					count := fileCount(len(files))
					if loading {
						count = i18n.T("Loading…")
					}
					muted(c, count).Padding(4, 8)
					ui.Scroll(c.Key("files")).Grow(1).Children(func() {
						for ix := range files {
							if fileRow(c, &files[ix], ix == selected).Clicked() {
								choose(ix)
							}
						}
					})
				})
			}, func() {
				ui.Column(c).Fill().Children(func() { diff.View(c) })
			}).Fill()
		})
	}})
}
