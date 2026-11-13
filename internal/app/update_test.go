package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/settings"
	"github.com/Zenyet/banyan/internal/update"
)

func TestUpdatesSettingsRenders(t *testing.T) {
	s := settings.Default()
	s.LastUpdateCheck = 1700000000
	sw := &settingsWindow{}
	tt := ui.NewTester(func(c *ui.Context) { sw.updates(c, &s) }, 820, 700)
	texts := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{"Software Updates", "Current version", "Check Now", Version} {
		if !strings.Contains(texts, want) {
			t.Errorf("updates page lacks %q:\n%s", want, texts)
		}
	}
}

func TestUpdateDialogShowsARelease(t *testing.T) {
	oldVersion, oldOpen := Version, updateDialogOpen
	Version, updateDialogOpen = "0.4.1", false
	defer func() { Version, updateDialogOpen = oldVersion, oldOpen }()
	st := &updateDialog{manifest: &update.Manifest{
		Version:  "0.4.2",
		NotesURL: "https://example.test/notes",
		Platforms: map[string]update.Asset{
			update.Platform(): {URL: "https://example.test/update", SHA256: strings.Repeat("0", 64)},
		},
	}}
	s := &Shell{content: func(*ui.Context) {}}
	openUpdateDialog(s, st)
	tt := ui.NewTester(s.view, 900, 700)
	texts := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{"Software Update", "Banyan 0.4.2 is available", "Install and Restart", "Release Notes"} {
		if !strings.Contains(texts, want) {
			t.Errorf("dialog lacks %q:\n%s", want, texts)
		}
	}
}
