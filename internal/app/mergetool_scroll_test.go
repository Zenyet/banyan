package app

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/conflict"
)

// mergeToolWith is a merge tool of one conflict of long lines.
func mergeToolWith(t *testing.T, lines int) (*mergeTool, *ui.Tester) {
	t.Helper()
	var ours, theirs []string
	for i := range lines {
		tail := fmt.Sprintf(" // ours %d %s\n", i, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
		ours = append(ours, fmt.Sprintf("public void method%d(String argument, int value) {%s", i, tail))
		theirs = append(theirs, fmt.Sprintf("public void method%d(String argument, int value) { // theirs %d\n", i, i))
	}
	cf := &conflict.Conflict{Ours: ours, Base: []string{"base\n"}, Theirs: theirs}
	mt := &mergeTool{
		path: "a.java", ours: "yours", theirs: "theirs",
		segments:    []conflict.Segment{{Conflict: cf}},
		resolutions: []*conflict.Resolution{nil},
		editing:     -1,
		unfolded:    map[int]bool{},
	}
	s := &Shell{content: func(*ui.Context) {}}
	s.Open(&Dialog{Title: "Merge", Width: 1280, Body: mt.view})
	tt := ui.NewTester(s.view, 1400, 900)
	tt.Frame()
	tt.Frame()
	return mt, tt
}

func TestMergeToolScrollsBothWays(t *testing.T) {
	mt, tt := mergeToolWith(t, 60)
	first, ok := tt.Find("public void method0(String argument, int value) { // ours 0 xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	if !ok {
		t.Fatalf("no first line: %q", tt.Texts())
	}
	if first.H > mergeLineHeight+2 {
		t.Fatalf("the long line wrapped to %v high", first.H)
	}
	last, _ := tt.Find("public void method59(String argument, int value) { // theirs 59")
	before := tt.Image()

	tt.Scroll(first.X+20, first.Y+first.H/2, 300, 0)
	tt.Frame()
	if mt.hx <= 0 {
		t.Fatal("the wheel did not pan the code sideways")
	}
	if bytes.Equal(before.Pix, tt.Image().Pix) {
		t.Fatal("the sideways pan did not move the code")
	}
	hx := mt.hx

	tt.Scroll(first.X+20, first.Y+first.H/2, 0, 300)
	tt.Frame()
	if mt.hx != hx {
		t.Fatalf("the sideways pan moved to %v on a wheel without it", mt.hx)
	}
	after, _ := tt.Find("public void method59(String argument, int value) { // theirs 59")
	if after.Y >= last.Y {
		t.Fatalf("the wheel did not scroll down: line 59 at %v, before %v", after.Y, last.Y)
	}

	// A diagonal wheel both pans and scrolls, rather than one swallowing
	// the other.
	tt.Scroll(first.X+20, first.Y+first.H/2, -100, 100)
	tt.Frame()
	if mt.hx >= hx {
		t.Fatalf("the diagonal wheel did not pan back: %v, before %v", mt.hx, hx)
	}
	diagonal, _ := tt.Find("public void method59(String argument, int value) { // theirs 59")
	if diagonal.Y >= after.Y {
		t.Fatalf("the diagonal wheel did not scroll down: line 59 at %v, before %v", diagonal.Y, after.Y)
	}
}
