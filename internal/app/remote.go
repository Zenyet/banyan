package app

import (
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

// lastSSH is the host and path of the most recently opened repository over
// SSH, from the recent repositories.
func lastSSH() (host, path string) {
	for _, location := range settings.Get().Recent {
		if h, p, ok := git.ParseLocation(location); ok {
			return h, p
		}
	}
	return "", ""
}

// openSSHRepo asks for a host and a path, and opens the repository there.
func openSSHRepo(s *Shell) {
	if s == nil {
		return
	}
	host, path := lastSSH()
	connecting, problem := false, ""
	s.Open(&Dialog{Title: i18n.T("Open Repository over SSH"), Width: 560, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		start := func() {
			h, p := strings.TrimSpace(host), strings.TrimSpace(path)
			if h == "" || p == "" || connecting {
				return
			}
			connecting, problem = true, ""
			go func() {
				repo, err := git.OpenSSH(h, p)
				model.Post(func() {
					connecting = false
					if err != nil {
						problem = err.Error()
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
		muted(c, i18n.T("Uses your ssh configuration: keys, agent, ProxyJump and ControlMaster from ~/.ssh/config."))
		muted(c, i18n.T("Host"))
		h := ui.TextInput(c.Key("host"), &host).Placeholder("user@dev-machine").AutoFocus().FillWidth()
		if h.Submitted() {
			start()
		}
		muted(c, i18n.T("Repository Path (on the remote host)"))
		p := ui.TextInput(c.Key("path"), &path).Placeholder("/home/me/project or ~/project").FillWidth()
		if p.Submitted() {
			start()
		}
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
			if primaryButton(c.Key("open"), i18n.T("Open")).Disabled(connecting ||
				strings.TrimSpace(host) == "" || strings.TrimSpace(path) == "").Clicked() {
				start()
			}
		})
	}})
}
