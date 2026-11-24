package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Every dialog has the window's close control, and it closes the dialog as
// Escape does.
func TestDialogClosesFromItsWindowButton(t *testing.T) {
	dismissed := false
	s := &Shell{content: func(*ui.Context) {}}
	s.Open(&Dialog{
		Title:     "A Dialog",
		Width:     400,
		OnDismiss: func() { dismissed = true },
		Body:      func(c *ui.Context, d *Dialog) { ui.Text(c, "Content") },
	})
	tt := ui.NewTester(s.view, 800, 600)
	tt.Frame()
	if !tt.HasText("A Dialog") {
		t.Fatalf("no dialog: %q", tt.Texts())
	}
	if err := tt.Click("Close"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(s.dialogs) != 0 {
		t.Fatal("the dialog stayed open")
	}
	if !dismissed {
		t.Fatal("OnDismiss did not run")
	}
}
