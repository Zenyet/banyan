package conflict

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Zenyet/banyan/internal/git"
)

func markers(c byte, label string) string {
	m := strings.Repeat(string(c), git.MarkerSize)
	if label == "" {
		return m + "\n"
	}
	return m + " " + label + "\n"
}

func sample() string {
	return "fn main() {\n" + markers('<', "yours") + "    a();\n" + markers('|', "base") + "    x();\n" +
		markers('=', "") + "    b();\n" + markers('>', "theirs") + "}\n"
}

func TestParsesCleanTextAndConflicts(t *testing.T) {
	want := []Segment{
		{Clean: []string{"fn main() {\n"}},
		{Conflict: &Conflict{Ours: []string{"    a();\n"}, Base: []string{"    x();\n"}, Theirs: []string{"    b();\n"}}},
		{Clean: []string{"}\n"}},
	}
	if got := Parse(sample()); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestGitSizedMarkersAreContent(t *testing.T) {
	text := "<<<<<<< HEAD\na\n=======\nb\n>>>>>>> other\n"
	if got := Parse(text); !reflect.DeepEqual(got, []Segment{{Clean: SplitLines(text)}}) {
		t.Fatalf("got %+v", got)
	}
}

func TestResultNeedsEveryConflictResolved(t *testing.T) {
	segments := Parse(sample())
	if _, ok := ResultText(segments, []*Resolution{nil}); ok {
		t.Fatal("unresolved")
	}
	check := func(r Resolution, want string) {
		t.Helper()
		if got, _ := ResultText(segments, []*Resolution{&r}); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
	check(Resolution{Kind: Theirs}, "fn main() {\n    b();\n}\n")
	check(Resolution{Kind: OursThenTheirs}, "fn main() {\n    a();\n    b();\n}\n")
	check(Resolution{Kind: Custom, Text: "    c();"}, "fn main() {\n    c();\n}\n")
}

func TestBothSidesKeepALineBreakBetweenThem(t *testing.T) {
	c := Conflict{Ours: []string{"a"}, Theirs: []string{"b"}}
	if got := c.Resolved(Resolution{Kind: OursThenTheirs}); !reflect.DeepEqual(got, []string{"a\n", "b"}) {
		t.Fatalf("got %q", got)
	}
}

func TestSplitLines(t *testing.T) {
	if got := SplitLines("a\nb"); !reflect.DeepEqual(got, []string{"a\n", "b"}) {
		t.Errorf("got %q", got)
	}
	if got := SplitLines(""); len(got) != 0 {
		t.Errorf("got %q", got)
	}
	if got := SplitLines("a\n"); !reflect.DeepEqual(got, []string{"a\n"}) {
		t.Errorf("got %q", got)
	}
}
