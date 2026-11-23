package app

import (
	"path"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/settings"
)

// openLocation opens a repository named by a location: a folder on this
// machine, or ssh://user@host/path for one on another, which connects in
// the background.
func openLocation(s *Shell, location string, replace bool) {
	if _, _, ok := git.ParseLocation(location); !ok {
		openPath(s, location, replace)
		return
	}
	go func() {
		repo, err := git.OpenLocation(location)
		mygo.RunOnMain(func() {
			if err != nil {
				toastOn(s, "error", err.Error())
				return
			}
			openRepos([]*git.Repo{repo}, false)
			if replace && s != nil && s.Win != nil {
				s.Win.Close()
			}
		})
	}()
}

// openRepoAt opens the repository at path on the machine repo is on, which
// may be another one: a worktree or submodule of it.
func openRepoAt(s *Shell, repo *git.Repo, path string, replace bool) {
	if !repo.Remote() {
		openPath(s, path, replace)
		return
	}
	go func() {
		opened, err := repo.OpenAt(path)
		mygo.RunOnMain(func() {
			if err != nil {
				toastOn(s, "error", err.Error())
				return
			}
			openRepos([]*git.Repo{opened}, false)
			if replace && s != nil && s.Win != nil {
				s.Win.Close()
			}
		})
	}()
}

// toastOn reports a problem in s, or in the window in front when s has
// none.
func toastOn(s *Shell, kind, message string) {
	if s == nil || s.Win == nil {
		s = shellOf(mygo.FocusedWindow())
	}
	if s == nil {
		for _, any := range shells {
			s = any
			break
		}
	}
	if s != nil {
		s.Toast(kind, message)
	}
}

// lastSSHHost is the host of the most recently opened repository over SSH.
func lastSSHHost() string {
	for _, location := range settings.Get().Recent {
		if host, _, ok := git.ParseLocation(location); ok {
			return host
		}
	}
	return ""
}

// openSSHRepo asks for a host, connects, and then lets the user choose the
// repository's folder there.
func openSSHRepo(s *Shell) {
	if s == nil {
		return
	}
	host := lastSSHHost()
	connecting, problem := false, ""
	s.Open(&Dialog{Title: i18n.T("Open Repository over SSH"), Width: 520, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		start := func() {
			h := strings.TrimSpace(host)
			if h == "" || connecting {
				return
			}
			connecting, problem = true, ""
			go func() {
				ssh, err := git.ConnectSSH(h)
				model.Post(func() {
					connecting = false
					if err != nil {
						problem = err.Error()
						return
					}
					if !d.open {
						return
					}
					d.Close()
					chooseRemoteDir(s, ssh)
				})
			}()
		}
		muted(c, i18n.T("Uses your ssh configuration: keys, agent, ProxyJump and ControlMaster from ~/.ssh/config."))
		muted(c, i18n.T("Host"))
		in := ui.TextInput(c.Key("host"), &host).Placeholder("user@dev-machine").AutoFocus().FillWidth()
		if in.Submitted() {
			start()
		}
		muted(c, i18n.T("After connecting, choose the repository folder."))
		if problem != "" {
			ui.Text(c, problem).TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if connecting {
				muted(c, i18n.T("Connecting…"))
			}
			if button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
				d.Close()
			}
			if primaryButton(c.Key("connect"), i18n.T("Connect")).Disabled(connecting || strings.TrimSpace(host) == "").Clicked() {
				start()
			}
		})
	}})
}

// chooseRemoteDir shows the folders of a host, from its home folder, for
// the user to choose the repository's folder: one ssh round trip per folder
// shown.
func chooseRemoteDir(s *Shell, ssh *git.SSH) {
	dir := "~"
	var folders []string
	loading, opening, problem := true, false, ""
	gen := 0
	load := func(target string) {
		target = strings.TrimSpace(target)
		if target == "" {
			return
		}
		gen++
		g := gen
		loading, problem = true, ""
		go func() {
			resolved, names, err := ssh.Dirs(target)
			model.Post(func() {
				if g != gen {
					return
				}
				loading = false
				if err != nil {
					problem = err.Error()
					return
				}
				dir, folders = resolved, names
			})
		}()
	}
	load(dir)
	s.Open(&Dialog{Title: i18n.Tf("Choose a Directory on {}", ssh.Host), Width: 620, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		open := func() {
			if loading || opening || dir == "" {
				return
			}
			opening, problem = true, ""
			target := dir
			go func() {
				repo, err := ssh.Open(target)
				model.Post(func() {
					opening = false
					if err != nil {
						problem = err.Error()
						return
					}
					if !d.open {
						return
					}
					d.Close()
					openRepos([]*git.Repo{repo}, false)
				})
			}()
		}
		muted(c, i18n.T("Choose the folder of a repository, or one of its subfolders."))
		ui.Row(c).Gap(8).Children(func() {
			in := ui.TextInput(c.Key("path"), &dir).Placeholder("~/project").Grow(1)
			if in.Submitted() {
				load(dir)
			}
			if button(c.Key("go"), i18n.T("Go")).Clicked() {
				load(dir)
			}
		})
		ui.Scroll(c.Key("folders")).MaxHeight(300).Shrink(1).MinHeight(120).Border(1, t.Border).Radius(t.Radius).Padding(4).Children(func() {
			if loading {
				muted(c, i18n.T("Loading…")).Padding(8)
				return
			}
			if up := path.Dir(dir); dir != "/" && up != dir {
				folderRow(c, "..", "chevrons-left", func() { load(up) })
			}
			for _, name := range folders {
				name := name
				folderRow(c, name, "folder-open", func() { load(path.Join(dir, name)) })
			}
			if len(folders) == 0 {
				muted(c, i18n.T("No folders here")).Padding(8)
			}
		})
		if problem != "" {
			ui.Text(c, problem).TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if loading || opening {
				muted(c, i18n.T("Loading…"))
			}
			if button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
				d.Close()
			}
			if primaryButton(c.Key("open"), i18n.T("Open")).Disabled(loading || opening || dir == "").Clicked() {
				open()
			}
		})
	}})
}

// folderRow is a folder in the remote directory chooser, which the user
// clicks to go into it.
func folderRow(c *ui.Context, label, iconName string, enter func()) {
	t := c.Theme()
	row := ui.Row(c.Key(label)).PaddingX(8).PaddingY(6).Gap(8).Radius(6).Cursor(ui.CursorPointer)
	if row.Hovered() {
		row.Background(hoverBG(t))
	}
	if row.Clicked() {
		enter()
	}
	row.Children(func() {
		ui.Icon(c, icon(iconName)).TextColor(t.TextMuted)
		ui.Text(c, label).SingleLine().Shrink(1).MinWidth(0)
	})
}
