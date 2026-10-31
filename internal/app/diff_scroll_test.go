package app

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// The wheel scrolls a diff over a hunk's header too, not only over its
// lines.
func TestDiffScrollsOverAHunkHeader(t *testing.T) {
	var patch strings.Builder
	patch.WriteString("diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n")
	for h := range 12 {
		fmt.Fprintf(&patch, "@@ -%d,6 +%d,6 @@ hunk %d\n", 1+h*100, 1+h*100, h)
		for i := range 6 {
			fmt.Fprintf(&patch, "-old %d of hunk %d\n+line %d of hunk %d\n", i, h, i, h)
		}
	}
	files, err := editor.ParsePatch(patch.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, layout := range []editor.DiffLayout{editor.DiffSplit, editor.DiffUnified} {
		d := &editor.DiffState{RenderSeparator: separator}
		d.SetPatch(files[0])
		d.Layout = layout
		tt := ui.NewTester(func(c *ui.Context) { editor.DiffView(c, d).Fill() }, 800, 600)
		tt.Frame()
		tt.Frame()
		header, ok := tt.Find("@@ -201,6 +201,6 @@ hunk 2")
		if !ok {
			t.Fatalf("layout %v: no header: %q", layout, tt.Texts())
		}
		tt.Scroll(header.X+20, header.Y+header.H/2, 0, 40)
		tt.Frame()
		// The editors keep their offset to themselves; MyGo's own tests
		// read it the same way.
		if y := reflect.ValueOf(d).Elem().FieldByName("eds").Index(1).FieldByName("scrollY").Float(); y < 39 {
			t.Errorf("layout %v: the wheel over a header scrolled to %v, want 40", layout, y)
		}
	}
}
