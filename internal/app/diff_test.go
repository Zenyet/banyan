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
	// A window of the tests: a shell without one.
	ws.log.diff.window = &Shell{}
	tt.Frame()
	if tt.HasText("Side-by-side") {
		t.Fatal("the diff is still under the log")
	}
	ws.log.diff.window = nil
	tt.Frame()
	if !tt.HasText("Side-by-side") {
		t.Fatal("the diff did not come back")
	}
}
