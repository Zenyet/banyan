package app

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/settings"
)

// reloader loads data for a dialog off the main thread, again whenever the
// repository changes.
type reloader[T any] struct {
	value   T
	loaded  bool
	version uint64
	status  int
	gen     int
}

func (r *reloader[T]) sync(m *model.Model, load func() T) {
	if r.loaded && r.version == m.RefsVersion && r.status == len(m.Status) {
		return
	}
	r.loaded, r.version, r.status = true, m.RefsVersion, len(m.Status)
	r.gen++
	gen := r.gen
	go func() {
		v := load()
		model.Post(func() {
			if gen == r.gen {
				r.value = v
			}
		})
	}()
}

// badge is a small label beside a name.
func badge(c *ui.Context, text string) {
	t := c.Theme()
	ui.Box(c).PaddingX(4).Radius(3).Background(t.SurfaceHover).Children(func() {
		ui.Text(c, text).FontSize(t.FontSize - 2)
	})
}

// suggestedPath is ~/x/repo-feature-login for ~/x/repo and branch
// feature/login.
func suggestedPath(root, branch string) string {
	safe := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, branch)
	return filepath.Join(filepath.Dir(root), filepath.Base(root)+"-"+safe)
}

// openWorktrees is "Manage Worktrees…": the repository's working trees,
// opening one in a window of its own, removing one, and adding a working
// tree for a branch, filled in for branch when given.
func openWorktrees(ws *Workspace, branch string) {
	m := ws.Model
	root := m.Repo.Root()
	path, pathEdited := "", false
	if branch != "" {
		path = suggestedPath(root, branch)
	}
	var trees reloader[[]git.Worktree]
	ws.Shell.Open(&Dialog{Title: i18n.T("Worktrees"), Width: 760, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		trees.sync(m, func() []git.Worktree { w, _ := m.Repo.Worktrees(); return w })
		muted(c, i18n.T("Each worktree is a folder with its own branch checked out, sharing this repository's history."))
		prunable := false
		ui.Scroll(c.Key("trees")).MaxHeight(300).Border(1, t.Border).Radius(t.Radius).Padding(4).Children(func() {
			for _, tree := range trees.value {
				prunable = prunable || tree.Prunable
				ui.Row(c.Key(tree.Path)).PaddingX(8).PaddingY(6).Gap(8).Children(func() {
					ui.Icon(c, icon("git-branch")).TextColor(t.TextMuted)
					ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
						ui.Row(c).Gap(4).Children(func() {
							head := tree.Branch
							if head == "" {
								head = i18n.Tf("detached at {}", git.Short(tree.Head))
							}
							ui.Text(c, head).FontWeight(600).SingleLine()
							if tree.IsCurrent {
								badge(c, i18n.T("current"))
							}
							if tree.IsMain {
								badge(c, i18n.T("main"))
							}
							if tree.Locked {
								badge(c, i18n.T("locked"))
							}
							if tree.Prunable {
								badge(c, i18n.T("folder missing"))
							}
						})
						muted(c, tilde(tree.Path)).SingleLine()
					})
					if !tree.IsCurrent && !tree.Prunable && ui.Button(c.Key("open"), i18n.T("Open")).Clicked() {
						openPath(ws.Shell, tree.Path, false)
					}
					if !tree.IsMain && !tree.IsCurrent && !tree.Prunable && iconButton(c.Key("remove"), "x", i18n.T("Remove Worktree")).Clicked() {
						p := tree.Path
						dirty := git.WorktreeIsDirty(p)
						message := i18n.Tf("Remove the worktree at {}? Its branch stays.", tilde(p))
						if dirty {
							message = i18n.Tf("{} has local changes. Remove it anyway? Its changes are lost.", tilde(p))
						}
						ws.Shell.Confirm(i18n.T("Remove Worktree"), message, i18n.T("Remove"), func() {
							ws.run(i18n.T("Remove worktree"), func(r *git.Repo) (string, error) { return r.RemoveWorktree(p, dirty) })
						})
					}
				})
			}
		})
		if prunable {
			ui.Row(c).Gap(8).Children(func() {
				muted(c, i18n.T("Some worktree folders are gone.")).Grow(1)
				if ui.Button(c.Key("prune"), i18n.T("Forget Missing")).Clicked() {
					ws.run(i18n.T("Prune worktrees"), (*git.Repo).PruneWorktrees)
				}
			})
		}
		ui.Text(c, i18n.T("New Worktree")).FontWeight(600)
		ui.Row(c).Gap(8).Children(func() {
			// The location follows the branch until it is typed in.
			if ui.TextInput(c.Key("branch"), &branch).Placeholder(i18n.T("Branch")).Width(220).Changed() && !pathEdited {
				path = ""
				if b := strings.TrimSpace(branch); b != "" {
					path = suggestedPath(root, b)
				}
			}
			if ui.TextInput(c.Key("path"), &path).Placeholder(i18n.T("Location")).Grow(1).Changed() {
				pathEdited = true
			}
			if ui.PrimaryButton(c.Key("add"), i18n.T("Add")).Clicked() {
				b, p := strings.TrimSpace(branch), strings.TrimSpace(path)
				if b == "" || p == "" {
					m.Fail(errorf("%s", i18n.T("Enter a branch and a location")))
					return
				}
				branch, path, pathEdited = "", "", false
				model.RunThen(m, i18n.Tf("Add worktree {}", b), func(r *git.Repo) (*git.Repo, error) { return r.AddWorktree(p, b) },
					func(repo *git.Repo) { openRepos([]*git.Repo{repo}, false) },
					func(err error) model.Event {
						return model.Event{Kind: model.OpFailed, Text: i18n.T("Adding the worktree failed") + "\n" + err.Error()}
					})
			}
		})
		muted(c, i18n.T("An existing branch is checked out there; a new name creates a branch from the current commit."))
	}})
}

// openSubmodules is "Submodules…": the repository's submodules with their
// state, updating them to the recorded commits, and opening one as a
// repository of its own.
func openSubmodules(ws *Workspace) {
	m := ws.Model
	var subs reloader[[]git.Submodule]
	update := func(paths []string) {
		label := i18n.T("Update submodules")
		if len(paths) == 1 {
			label = i18n.Tf("Update submodule {}", paths[0])
		}
		ws.run(label, func(r *git.Repo) (string, error) { return r.UpdateSubmodules(paths) })
	}
	ws.Shell.Open(&Dialog{Title: i18n.T("Submodules"), Width: 680, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		subs.sync(m, func() []git.Submodule { s, _ := m.Repo.Submodules(); return s })
		needsUpdate := false
		switch {
		case subs.value == nil && !subs.loaded:
			muted(c, i18n.T("Loading…"))
		case len(subs.value) == 0:
			muted(c, i18n.T("This repository has no submodules."))
		default:
			ui.Scroll(c.Key("subs")).MaxHeight(360).Border(1, t.Border).Radius(t.Radius).Padding(4).Children(func() {
				for _, sub := range subs.value {
					state, color := i18n.T("up to date"), t.Success
					switch sub.State {
					case git.SubmoduleOutOfDate:
						state, color = i18n.T("at another commit"), t.Warning
					case git.SubmoduleUninitialized:
						state, color = i18n.T("not initialized"), t.TextMuted
					case git.SubmoduleConflict:
						state, color = i18n.T("conflict"), t.Danger
					}
					needsUpdate = needsUpdate || sub.State != git.SubmoduleUpToDate
					ui.Row(c.Key(sub.Path)).PaddingX(8).PaddingY(6).Gap(8).Children(func() {
						ui.Icon(c, icon("folder-open")).TextColor(t.TextMuted)
						ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
							ui.Text(c, sub.Path).FontWeight(600).SingleLine()
							ui.Row(c).Gap(8).FontSize(t.FontSize - 1).Children(func() {
								ui.Text(c, state).TextColor(color)
								ui.Text(c, git.Short(sub.Commit)).TextColor(t.TextMuted)
							})
						})
						if sub.State != git.SubmoduleUpToDate && ui.Button(c.Key("update"), i18n.T("Update")).Clicked() {
							update([]string{sub.Path})
						}
						if sub.State != git.SubmoduleUninitialized && ui.Button(c.Key("open"), i18n.T("Open")).Clicked() {
							openPath(ws.Shell, filepath.Join(m.Repo.Root(), filepath.FromSlash(sub.Path)), false)
						}
					})
				}
			})
		}
		ui.Row(c).Gap(8).Children(func() {
			muted(c, i18n.T("Updating clones missing submodules and checks each out at the commit this repository records.")).Grow(1).Shrink(1)
			if ui.PrimaryButton(c.Key("all"), i18n.T("Update All")).Disabled(!needsUpdate).Clicked() {
				update(nil)
			}
		})
	}})
}

// openRemotes is "Manage Remotes…": add, edit, rename and remove remotes.
func openRemotes(ws *Workspace) {
	m := ws.Model
	var remotes reloader[[]git.Remote]
	editing, name, url := "", "", ""
	ws.Shell.Open(&Dialog{Title: i18n.T("Git Remotes"), Width: 720, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		remotes.sync(m, func() []git.Remote { r, _ := m.Repo.Remotes(); return r })
		ui.Scroll(c.Key("remotes")).MaxHeight(280).Border(1, t.Border).Radius(t.Radius).Children(func() {
			if len(remotes.value) == 0 {
				muted(c, i18n.T("No remotes")).Padding(12)
			}
			for _, r := range remotes.value {
				row := ui.Row(c.Key(r.Name)).PaddingX(12).PaddingY(8).Gap(12).BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
				if editing == r.Name {
					row.Background(selectionBG)
				}
				row.Children(func() {
					ui.Text(c, r.Name).Width(110).FontWeight(600).SingleLine()
					ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
						ui.Text(c, r.FetchURL).SingleLine()
						if r.PushURL != r.FetchURL {
							muted(c, "push: "+r.PushURL).SingleLine()
						}
					})
					if iconButton(c.Key("edit"), "pencil", i18n.T("Edit")).Clicked() {
						editing, name, url = r.Name, r.Name, r.FetchURL
					}
					if iconButton(c.Key("remove"), "trash", i18n.T("Remove")).Clicked() {
						n := r.Name
						ws.Shell.Confirm(i18n.T("Remove Remote"), i18n.Tf("Remove remote '{}' and its remote-tracking branches?", n), i18n.T("Remove"), func() {
							ws.run(i18n.Tf("Remove remote {}", n), func(r *git.Repo) (string, error) { return r.RemoveRemote(n) })
						})
					}
				})
			}
		})
		if editing != "" {
			muted(c, i18n.Tf("Edit remote '{}'", editing))
		} else {
			muted(c, i18n.T("Add a remote"))
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.TextInput(c.Key("name"), &name).Placeholder("origin").Width(140)
			ui.TextInput(c.Key("url"), &url).Placeholder("https://github.com/owner/repo.git").Grow(1)
			if editing != "" && ui.Button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
				editing, name, url = "", "", ""
			}
			label := i18n.T("Add")
			if editing != "" {
				label = i18n.T("Save")
			}
			if ui.PrimaryButton(c.Key("save"), label).Clicked() {
				n, u, old := strings.TrimSpace(name), strings.TrimSpace(url), editing
				if n == "" || u == "" {
					return
				}
				if old == "" {
					ws.run(i18n.Tf("Add remote {}", n), func(r *git.Repo) (string, error) { return r.AddRemote(n, u) })
				} else {
					ws.run(i18n.Tf("Update remote {}", n), func(r *git.Repo) (string, error) {
						if old != n {
							if _, err := r.RenameRemote(old, n); err != nil {
								return "", err
							}
						}
						return r.SetRemoteURL(n, u)
					})
				}
				editing, name, url = "", "", ""
			}
		})
		ui.Row(c).Justify(ui.End).Children(func() {
			if ui.Button(c.Key("close"), i18n.T("Close")).Clicked() {
				d.Close()
			}
		})
	}})
}

// defaultCloneParent is where new clones go: next to the most recently
// opened repository.
func defaultCloneParent() string {
	if recent := settings.Get().Recent; len(recent) > 0 {
		return filepath.Dir(recent[0])
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "."
}

// openClone is "Clone Repository…": URL and destination, then open the
// clone, closing the welcome window if it was asked there.
func openClone(s *Shell) {
	if s == nil {
		return
	}
	url, dir := "", defaultCloneParent()
	dirEdited, cloning := false, false
	cloneErr := ""
	s.Open(&Dialog{Title: i18n.T("Clone Repository"), Width: 560, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		start := func() {
			u, dest := strings.TrimSpace(url), strings.TrimSpace(dir)
			if u == "" || cloning {
				return
			}
			if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
				cloneErr = i18n.Tf("{} already exists and is not empty", dest)
				return
			}
			cloning, cloneErr = true, ""
			go func() {
				repo, err := git.Clone(u, dest)
				model.Post(func() {
					cloning = false
					if err != nil {
						cloneErr = err.Error()
						return
					}
					d.Close()
					openRepos([]*git.Repo{repo}, false)
					if s == welcome {
						closeWelcome()
					}
				})
			}()
		}
		muted(c, "URL")
		u := ui.TextInput(c.Key("url"), &url).Placeholder("https://github.com/owner/repo.git").AutoFocus().FillWidth()
		if u.Changed() && !dirEdited {
			dir = filepath.Join(defaultCloneParent(), git.CloneDirName(url))
		}
		if u.Submitted() {
			start()
		}
		muted(c, i18n.T("Directory"))
		ui.Row(c).Gap(8).Children(func() {
			in := ui.TextInput(c.Key("dir"), &dir).Grow(1)
			if in.Changed() {
				dirEdited = true
			}
			if in.Submitted() {
				start()
			}
			if ui.Button(c.Key("browse"), i18n.T("Browse…")).Clicked() {
				go func() {
					paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: s.Win, Title: i18n.T("Clone Into"), Directory: true})
					if err != nil || len(paths) == 0 {
						return
					}
					model.Post(func() {
						dirEdited = true
						dir = filepath.Join(paths[0], git.CloneDirName(url))
					})
				}()
			}
		})
		if cloneErr != "" {
			ui.Text(c, cloneErr).TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if cloning {
				muted(c, i18n.T("Cloning…"))
			}
			if ui.Button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
				d.Close()
			}
			if ui.PrimaryButton(c.Key("clone"), i18n.T("Clone")).Disabled(cloning).Clicked() {
				start()
			}
		})
	}})
}
