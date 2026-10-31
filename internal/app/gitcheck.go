package app

import (
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/settings"
)

// gitDownload is where Git is downloaded for every system.
const gitDownload = "https://git-scm.com/downloads"

// checkGit says, over the window in front, that git cannot run, and how to
// get it: Banyan runs git for everything it does.
func checkGit() {
	program := settings.Read(func(s *settings.Settings) string { return s.GitProgram() })
	go func() {
		if _, err := git.GitVersion(program); err == nil {
			return
		}
		mygo.RunOnMain(func() {
			if s := frontShell(); s != nil {
				openGitMissing(s, program)
			}
		})
	}()
}

// frontShell is the shell of the window in front, else of any window.
func frontShell() *Shell {
	if s := shellOf(mygo.FocusedWindow()); s != nil {
		return s
	}
	if welcome != nil {
		return welcome
	}
	for _, s := range shells {
		return s
	}
	return nil
}

// gitInstall is how to install Git here: what to do, and a command doing
// it.
func gitInstall() (how, command string) {
	switch runtime.GOOS {
	case "windows":
		return i18n.T("Install Git for Windows from its website, or with this command:"), "winget install --id Git.Git -e"
	case "darwin":
		return i18n.T("Install Apple's command line tools, which include Git, with this command, or Git from its website:"), "xcode-select --install"
	}
	return i18n.T("Install Git with your distribution's package manager, such as:"), "sudo apt install git"
}

func openGitMissing(s *Shell, program string) {
	still := false
	s.Open(&Dialog{Title: i18n.T("Banyan Needs Git"), Width: 500, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		how, command := gitInstall()
		ui.Text(c, i18n.Tf("Banyan runs Git for everything it does, and cannot run {}.", program))
		ui.Text(c, how)
		ui.Text(c, command).Font(monoFont).Padding(8, 12).Radius(8).Background(t.Surface).Selectable()
		muted(c, i18n.T("If Git is installed somewhere else, give its path in Settings › Version Control."))
		if still {
			ui.Text(c, i18n.T("Git still cannot run. If you just installed it, quit and reopen Banyan.")).TextColor(t.Warning)
		}
		ui.Row(c).Gap(8).Margin(8, 0, 0, 0).Children(func() {
			if button(c.Key("download"), i18n.T("Download Git")).Clicked() {
				mygo.Shell.OpenExternal(gitDownload)
			}
			if button(c.Key("settings"), i18n.T("Settings…")).Clicked() {
				openSettings()
			}
			ui.Spacer(c)
			if button(c.Key("close"), i18n.T("Close")).Clicked() {
				d.Close()
			}
			if primaryButton(c.Key("again"), i18n.T("Check Again")).Clicked() {
				settings.FindGitAgain()
				program = settings.Read(func(s *settings.Settings) string { return s.GitProgram() })
				if _, err := git.GitVersion(program); err == nil {
					d.Close()
					s.Toast("success", i18n.Tf("Found Git: {}", program))
				} else {
					still = true
				}
			}
		})
	}})
}
