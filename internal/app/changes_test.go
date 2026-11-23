package app

import (
	"strings"
	"testing"
)

func TestChecksCommitMessages(t *testing.T) {
	cases := map[string]int{
		"":                                   0,
		"Fix the thing\n\nBecause it broke.": 0,
		strings.Repeat("x", 73):              1,
		strings.Repeat("字", 72):              0,
		"Subject\nBody right away":           1,
		strings.Repeat("y", 80) + "\nbody":   2,
	}
	for message, want := range cases {
		if got := len(messageProblems(message)); got != want {
			t.Errorf("%q: %d problems, want %d", message, got, want)
		}
	}
}

// Choosing the file shown again keeps its diff, rather than reload it.
func TestChangesKeepsTheDiffOfTheFileChosenAgain(t *testing.T) {
	ws := branchesWorkspace(t)
	cv := newChangesView(ws)
	cv.selected = "a.txt"
	generation := cv.diff.generation
	cv.selectPath("a.txt")
	if cv.diff.generation != generation {
		t.Fatal("the diff loaded again")
	}
}
