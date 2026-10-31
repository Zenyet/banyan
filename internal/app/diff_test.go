package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// A diff that takes a while says it's loading, rather than leave the
// previous file's diff under the new title.
func TestSlowDiffSaysItIsLoading(t *testing.T) {
	const patch = "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old line\n+new line\n"
	p := &DiffPane{Remote: true}
	p.Load("a.txt", func() (string, error) { return patch, nil })
	// model.Post runs at once outside the app, on the loader's goroutine.
	for deadline := time.Now().Add(2 * time.Second); p.loading && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if p.state == nil {
		t.Fatal("the first diff loaded")
	}
	release := make(chan struct{})
	p.Load("b.txt", func() (string, error) {
		<-release
		return patch, nil
	})
	tt := ui.NewTester(p.View, 800, 400)
	message := "Downloading the file's contents from the remote (this repository is a partial clone)…"
	if tt.HasText(message) {
		t.Fatalf("a load that is quick so far shows no message: %q", tt.Texts())
	}
	time.Sleep(diffLoadingDelay + 50*time.Millisecond)
	tt.Frame()
	if !tt.HasText(message) {
		t.Fatalf("no loading message: %q", tt.Texts())
	}
	close(release)
}

// A diff in a window of its own leaves the view it belongs to without
// it, and comes back there once it is docked.
func TestDetachedDiffLeavesTheLog(t *testing.T) {
	ws := branchesWorkspace(t)
	tt := ui.NewTester(func(c *ui.Context) { ws.log.View(c) }, 1200, 800)
	if !tt.HasText("Side-by-side") {
		t.Fatalf("no diff under the log: %q", tt.Texts())
	}
	// In a window, which the tests have none of: as hidden.
	ws.log.diff.inWindow = true
	tt.Frame()
	if tt.HasText("Side-by-side") {
		t.Fatal("the diff is still under the log")
	}
	ws.log.diff.inWindow = false
	tt.Frame()
	if !tt.HasText("Side-by-side") {
		t.Fatal("the diff did not come back")
	}
}

// The diff hidden leaves the log the whole height, and a file
// double-clicked in the commit's changed files shows it again.
func TestHiddenDiffComesBackOnDoubleClick(t *testing.T) {
	ws := branchesWorkspace(t)
	tt := ui.NewTester(func(c *ui.Context) { ws.log.View(c) }, 1200, 800)
	ws.log.reload()
	for deadline := time.Now().Add(5 * time.Second); len(ws.log.rows) == 0 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		tt.Frame()
	}
	ws.log.selectedRow = 0
	ws.log.diff.Hide()
	tt.Frame()
	if tt.HasText("Side-by-side") {
		t.Fatal("the diff still shows")
	}
	// The commit's details come off the main thread.
	var r ui.Rect
	found := false
	for deadline := time.Now().Add(5 * time.Second); !found && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		tt.Frame()
		r, found = tt.Find("notes.md")
	}
	if !found {
		t.Fatalf("no changed file: %q", tt.Texts())
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if !tt.HasText("Side-by-side") {
		t.Fatalf("the diff did not come back: %q", tt.Texts())
	}
	// The list chose the file too: its double click was the row's.
	if ws.log.fileRow < 0 || ws.log.fileRows[ws.log.fileRow].Node.Path != "notes.md" {
		t.Fatalf("the file double-clicked is not chosen: row %d", ws.log.fileRow)
	}
}
