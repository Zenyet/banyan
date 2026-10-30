// Package app is Banyan's interface, drawn by MyGo's native UI.
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/askpass"
	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/settings"
)

// Version is the app's version, which main sets.
var Version = "dev"

// quitting is set by Quit, so that windows closing on the way out don't
// bring back the welcome window.
var quitting bool

// Run runs the app, opening requested (a path given on the command line)
// if not empty.
func Run(requested string) error {
	mygo.App.SetName("Banyan")
	mygo.App.SetVersion(Version)
	i18n.Apply(settings.Read(func(s *settings.Settings) string { return s.Language }))
	applyAppearance()
	model.Post = func(fn func()) {
		go mygo.RunOnMain(func() {
			fn()
			for w := range shells {
				w.Invalidate()
			}
		})
	}
	mygo.App.WhenReady(func() {
		if requests, err := askpass.StartServer(); err == nil {
			go func() {
				for r := range requests {
					model.Post(func() { showCredentialPrompt(r) })
				}
			}()
		} else {
			fmt.Fprintln(os.Stderr, "credential prompts unavailable:", err)
		}
		setMenu()
		repos := initialRepos(requested)
		if len(repos) > 0 {
			openRepos(repos, false)
		} else {
			openWelcome()
			// A folder named on the command line that isn't a repository yet.
			if info, err := os.Stat(requested); requested != "" && err == nil && info.IsDir() {
				openPath(welcome, requested, true)
			}
		}
		// CI launches the app this way to check that it starts: it quits by
		// itself once its window has been up for a few seconds.
		if os.Getenv("BANYAN_SMOKE_TEST") != "" {
			go func() {
				time.Sleep(5 * time.Second)
				mygo.RunOnMain(func() {
					fmt.Printf("smoke test passed: %d window(s) open\n", len(mygo.Windows()))
					quit()
				})
			}()
		}
	})
	mygo.App.OnDidBecomeActive(func() {
		// Pick up changes made outside the app, like JetBrains does on frame
		// activation.
		if rw := focusedRepoWindow(); rw != nil && len(rw.tabs) > 0 {
			rw.tabs[rw.active].Model.RefreshIfStale()
		}
	})
	return mygo.App.Run()
}

func quit() {
	quitting = true
	mygo.App.Quit()
}

// windowClosed brings back the welcome window when the last repository
// closes; closing that quits.
func windowClosed() {
	if len(shells) > 0 {
		return
	}
	if quitting || welcomeClosedLast {
		quit()
		return
	}
	openWelcome()
}

// applyAppearance follows the theme of the settings.
func applyAppearance() {
	switch settings.Read(func(s *settings.Settings) settings.ThemeChoice { return s.Theme }) {
	case settings.ThemeLight:
		mygo.Theme.SetSource(mygo.ThemeLight)
	case settings.ThemeDark:
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

// changeSettings changes the settings and shows the change everywhere.
func changeSettings(f func(*settings.Settings)) {
	before := settings.Get()
	settings.Update(f)
	after := settings.Get()
	if after.Theme != before.Theme {
		applyAppearance()
	}
	if after.Language != before.Language {
		i18n.Apply(after.Language)
		setMenu()
	}
	if !slicesEqual(after.Shortcuts, before.Shortcuts) {
		setMenu()
	}
	for w := range shells {
		w.Invalidate()
	}
}

func slicesEqual(a, b []settings.Shortcut) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// initialRepos are the repositories to open at launch: the one named on the
// command line, the current directory, or (when launched from Finder) the
// tabs open last time, else the last repository opened.
func initialRepos(requested string) []*git.Repo {
	if requested != "" {
		if repo, err := git.Open(requested); err == nil {
			return []*git.Repo{repo}
		}
		return nil
	}
	if dir, err := os.Getwd(); err == nil && dir != "/" {
		if repo, err := git.Open(dir); err == nil {
			return []*git.Repo{repo}
		}
	}
	s := settings.Get()
	var session []*git.Repo
	for _, path := range s.Session {
		if repo, err := git.Open(path); err == nil {
			session = append(session, repo)
		}
	}
	if len(session) > 0 {
		return session
	}
	for _, path := range s.Recent {
		if repo, err := git.Open(path); err == nil {
			return []*git.Repo{repo}
		}
	}
	return nil
}

// promptOpenRepo asks for a repository folder and opens it, closing the
// window of s if it's the welcome window.
func promptOpenRepo(s *Shell) {
	var parent *mygo.Window
	if s != nil {
		parent = s.Win
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: i18n.T("Open Repository"), Directory: true})
		if err != nil || len(paths) == 0 {
			return
		}
		mygo.RunOnMain(func() { openPath(s, paths[0], s == welcome) })
	}()
}

// openPath opens the repository at path (a dropped or chosen folder, or a
// file in one), and closes the window of s if replace. A folder outside any
// repository can be made one.
func openPath(s *Shell, path string, replace bool) {
	dir := path
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		dir = filepath.Dir(path)
	}
	done := func(repo *git.Repo) {
		openRepos([]*git.Repo{repo}, false)
		if replace && s != nil {
			s.Win.Close()
		}
	}
	repo, err := git.Open(dir)
	switch {
	case err == nil:
		done(repo)
	case isDir(dir) && s != nil:
		s.Confirm(i18n.T("Not a Git Repository"), i18n.Tf("{} is not inside a Git repository. Create one there?", tilde(dir)), i18n.T("Create Repository"), func() {
			repo, err := git.Init(dir)
			if err != nil {
				s.Toast("error", err.Error())
				return
			}
			done(repo)
		})
	case s != nil:
		s.Toast("error", err.Error())
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// showCredentialPrompt asks for what git asks, in the window in front.
func showCredentialPrompt(r *askpass.Request) {
	s := shellOf(mygo.FocusedWindow())
	if s == nil {
		for _, any := range shells {
			s = any
			break
		}
	}
	if s == nil {
		// Without a window the request is cancelled.
		r.Reply(nil)
		return
	}
	secret := r.IsSecret()
	value := ""
	answered := false
	answer := func(v *string) {
		if !answered {
			answered = true
			r.Reply(v)
		}
	}
	s.Open(&Dialog{Title: "Git Authentication", Width: 460, OnDismiss: func() { answer(nil) }, Body: func(c *ui.Context, d *Dialog) {
		prompt := r.Prompt
		for len(prompt) > 0 && (prompt[len(prompt)-1] == ':' || prompt[len(prompt)-1] == ' ') {
			prompt = prompt[:len(prompt)-1]
		}
		ui.Text(c, prompt)
		input := ui.TextInput(c.Key("answer"), &value).AutoFocus().FillWidth()
		if secret {
			input.Password()
			muted(c, "For GitHub and GitLab over HTTPS, use a personal access token.")
		}
		submit := func() {
			v := value
			d.Close()
			answer(&v)
		}
		if input.Submitted() {
			submit()
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c.Key("cancel"), "Cancel").Clicked() {
				d.Close()
				answer(nil)
			}
			if ui.PrimaryButton(c.Key("ok"), "OK").Clicked() {
				submit()
			}
		})
	}})
}
