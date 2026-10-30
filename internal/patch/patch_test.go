package patch

import (
	"reflect"
	"strings"
	"testing"
)

const sample = `diff --git a/f.txt b/f.txt
index 1111111..2222222 100644
--- a/f.txt
+++ b/f.txt
@@ -1,3 +1,3 @@ fn first()
-one
+ONE
 two
 three
@@ -8,3 +8,4 @@ fn second()
 eight
 nine
+nine and a half
 ten
`

func TestParsesHunks(t *testing.T) {
	file := Parse(sample)
	if len(file.Header) != 4 || len(file.Hunks) != 2 {
		t.Fatalf("header %d, hunks %d", len(file.Header), len(file.Hunks))
	}
	h0, h1 := &file.Hunks[0], &file.Hunks[1]
	if h0.Context() != "fn first()" {
		t.Errorf("context %q", h0.Context())
	}
	if h0.Additions() != 1 || h0.Deletions() != 1 || h1.Additions() != 1 || h1.Deletions() != 0 {
		t.Error("counts")
	}
	if h1.NewStart() != 8 || h0.FirstChangedLine() != 1 || h1.FirstChangedLine() != 10 {
		t.Error("lines")
	}
}

func TestSelectsASubset(t *testing.T) {
	file := Parse(sample)
	second := file.Hunks[1].Header
	patch, ok := Select(file, func(h *Hunk, _ int) bool { return h.Header == second })
	if !ok || !strings.HasPrefix(patch, "diff --git a/f.txt b/f.txt\n") || !strings.Contains(patch, "+nine and a half\n") ||
		strings.Contains(patch, "+ONE") || strings.Contains(patch, "fn first()") {
		t.Errorf("patch:\n%s", patch)
	}
	if _, ok := Select(file, func(*Hunk, int) bool { return false }); ok {
		t.Error("nothing kept")
	}
}

func TestSelectsSingleLines(t *testing.T) {
	file := Parse(sample)
	first := file.Hunks[0]
	if !reflect.DeepEqual(first.Changes(), []int{0, 1}) || first.NewLineAt(1) != 1 || file.Hunks[1].NewLineAt(2) != 10 {
		t.Error("changes")
	}
	// Keep only the addition: the removal becomes context.
	patch, _ := Select(file, func(h *Hunk, ix int) bool { return h.Header == first.Header && ix == 1 })
	if !strings.Contains(patch, "@@ -1,3 +1,3 @@ fn first()\n one\n+ONE\n two\n") || strings.Contains(patch, "nine and a half") {
		t.Errorf("patch:\n%s", patch)
	}
	// Keep only the removal: the addition is dropped.
	patch, _ = Select(file, func(h *Hunk, ix int) bool { return h.Header == first.Header && ix == 0 })
	if !strings.Contains(patch, "fn first()\n-one\n two\n") {
		t.Errorf("patch:\n%s", patch)
	}
}

func TestNoNewlineMarkerFollowsItsLine(t *testing.T) {
	file := Parse("--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n")
	patch, _ := Select(file, func(_ *Hunk, ix int) bool { return ix == 0 })
	if !strings.HasSuffix(patch, "@@ -1 +1 @@\n-old\n\\ No newline at end of file\n") {
		t.Errorf("patch:\n%s", patch)
	}
}
