package app

import (
	"context"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/settings"
	"github.com/Zenyet/banyan/internal/update"
)

// The update state is read and changed on the main thread; the network
// and install work happen on goroutines, whose results come back through
// RunOnMain.
var (
	updateChecking   bool
	availableUpdate  *update.Manifest
	updateDialogOpen bool
)

const updatePage = "https://github.com/Zenyet/banyan/releases/latest"

// maybeCheckUpdates looks for a newer release after launch, at most once
// a day, when the settings allow it.
func maybeCheckUpdates() {
	if Version == "" || Version == "dev" {
		return
	}
	s := settings.Get()
	if !s.AutoCheckUpdates {
		return
	}
	if s.LastUpdateCheck != 0 && time.Since(time.Unix(s.LastUpdateCheck, 0)) < 24*time.Hour {
		return
	}
	checkForUpdates(false)
}

// checkForUpdates asks GitHub for the latest release. manual opens a
// dialog with the progress, or its result; an automatic check only shows
// a toast when a newer release is there.
func checkForUpdates(manual bool) {
	if Version == "" || Version == "dev" {
		if manual {
			if s := frontShell(); s != nil {
				s.Toast("", i18n.T("Development builds do not check for updates"))
			}
		}
		return
	}
	if updateChecking {
		if manual {
			if s := frontShell(); s != nil && !updateDialogOpen {
				if availableUpdate != nil {
					openUpdateDialog(s, &updateDialog{manifest: availableUpdate})
				}
			}
		}
		return
	}
	var st *updateDialog
	if manual {
		st = &updateDialog{checking: true}
		s := frontShell()
		if s == nil {
			return
		}
		openUpdateDialog(s, st)
	}
	updateChecking = true
	availableUpdate = nil
	invalidateAllWindows()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		m, err := update.Check(ctx, Version)
		mygo.RunOnMain(func() {
			updateChecking = false
			settings.Update(func(s *settings.Settings) {
				s.LastUpdateCheck = time.Now().Unix()
			})
			if st != nil {
				st.checking = false
				st.manifest = m
				st.upToDate = err == nil && m == nil
				if err != nil {
					st.err = err.Error()
				}
			}
			if m != nil {
				availableUpdate = m
				skipped := settings.Read(func(s *settings.Settings) bool { return s.SkippedUpdate == m.Version })
				if !manual && !skipped {
					if s := frontShell(); s != nil {
						version := m.Version
						s.ToastAction("", i18n.Tf("Banyan {} is available", version), i18n.T("Install…"),
							func() { openUpdateDialog(s, &updateDialog{manifest: m}) })
					}
				}
			}
			invalidateAllWindows()
		})
	}()
}

// invalidateAllWindows redraws windows after the update state changes.
func invalidateAllWindows() {
	for w := range shells {
		w.Invalidate()
	}
}

// updateDialog is what the Software Update dialog shows, by pointer, so
// that a check or download can change it as it goes.
type updateDialog struct {
	checking   bool
	installing bool
	upToDate   bool
	manifest   *update.Manifest
	err        string
}

func openUpdateDialog(s *Shell, st *updateDialog) {
	if s == nil || updateDialogOpen {
		return
	}
	updateDialogOpen = true
	s.Open(&Dialog{
		Title: i18n.T("Software Update"),
		Width: 540,
		OnDismiss: func() {
			updateDialogOpen = false
		},
		Body: func(c *ui.Context, d *Dialog) {
			t := c.Theme()
			close := func() {
				updateDialogOpen = false
				d.Close()
			}
			switch {
			case st.checking:
				ui.Row(c.Key("checking")).Gap(8).Children(func() {
					ui.Spinner(c).Size(16, 16)
					ui.Text(c, i18n.T("Checking for updates…"))
				})
				ui.Row(c).Justify(ui.End).Children(func() {
					if button(c.Key("close"), i18n.T("Close")).Clicked() {
						close()
					}
				})
				return
			case st.installing:
				ui.Row(c.Key("installing")).Gap(8).Children(func() {
					ui.Spinner(c).Size(16, 16)
					ui.Text(c, i18n.T("Downloading and installing…"))
				})
				muted(c, i18n.T("Banyan will restart when the update is ready."))
				return
			case st.err != "":
				ui.Text(c, i18n.T("Banyan could not check for updates.")).FontWeight(600)
				ui.Text(c, st.err).TextColor(t.Danger).Selectable().MaxLines(5)
				ui.Row(c).Gap(8).Justify(ui.End).Margin(8, 0, 0, 0).Children(func() {
					if button(c.Key("download"), i18n.T("Download")).Clicked() {
						mygo.Shell.OpenExternal(updatePage)
					}
					if button(c.Key("again"), i18n.T("Try Again")).Clicked() {
						close()
						checkForUpdates(true)
					}
					if button(c.Key("close"), i18n.T("Close")).Clicked() {
						close()
					}
				})
				return
			case st.upToDate:
				ui.Row(c.Key("uptodate")).Gap(8).Children(func() {
					ui.Icon(c, icon("circle-check")).TextColor(t.Success)
					ui.Text(c, i18n.Tf("Banyan {} is up to date.", Version))
				})
				ui.Row(c).Justify(ui.End).Children(func() {
					if button(c.Key("close"), i18n.T("Close")).Clicked() {
						close()
					}
				})
				return
			case st.manifest != nil:
				m := st.manifest
				ui.Text(c, i18n.Tf("Banyan {} is available", m.Version)).FontSize(t.FontSize + 2).Bold()
				muted(c, i18n.Tf("You have {}.", Version))
				_, installable := m.Asset()
				if !installable {
					ui.Text(c, i18n.Tf("There is no update package for this system ({})", update.Platform())).TextColor(t.Warning)
				}
				ui.Row(c).Gap(8).Margin(8, 0, 0, 0).Children(func() {
					if m.NotesURL != "" && button(c.Key("notes"), i18n.T("Release Notes")).Clicked() {
						mygo.Shell.OpenExternal(m.NotesURL)
					}
					ui.Spacer(c)
					if button(c.Key("later"), i18n.T("Later")).Clicked() {
						close()
					}
					if button(c.Key("skip"), i18n.T("Skip This Version")).Clicked() {
						version := m.Version
						changeSettings(func(s *settings.Settings) { s.SkippedUpdate = version })
						close()
					}
					if installable && primaryButton(c.Key("install"), i18n.T("Install and Restart")).Clicked() {
						st.installing = true
						st.err = ""
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
							defer cancel()
							err := update.Install(ctx, m)
							mygo.RunOnMain(func() {
								if err != nil {
									st.installing = false
									st.err = err.Error()
									invalidateAllWindows()
									return
								}
								quit()
							})
						}()
					}
				})
				return
			default:
				muted(c, i18n.T("No update was found."))
				ui.Row(c).Justify(ui.End).Children(func() {
					if button(c.Key("close"), i18n.T("Close")).Clicked() {
						close()
					}
				})
			}
		},
	})
}
