package graph

import (
	"fmt"
	"slices"
	"testing"

	"github.com/Zenyet/banyan/internal/git"
)

func commit(id string, parents ...string) git.Commit {
	return git.Commit{ID: id, Parents: parents}
}

func TestLinearHistoryStaysInOneLane(t *testing.T) {
	for _, row := range Layout([]git.Commit{commit("c", "b"), commit("b", "a"), commit("a")}, "") {
		if row.Node != 0 || row.Width != 1 {
			t.Fatalf("%+v", row)
		}
	}
}

func TestMergeOpensAndClosesALane(t *testing.T) {
	// m merges f into b; both come from a.
	rows := Layout([]git.Commit{commit("m", "b", "f"), commit("f", "a"), commit("b", "a"), commit("a")}, "")
	if rows[0].Node != 0 || !slices.Contains(rows[0].Edges(), Edge{0, 1, Bottom, 1}) {
		t.Fatal("m opens f's lane")
	}
	if rows[1].Node != 1 || rows[2].Node != 0 {
		t.Fatal("nodes")
	}
	// f's lane bends back into the first-parent lane at b.
	if !slices.ContainsFunc(rows[2].Edges(), func(e Edge) bool { return e.Half == Bottom && e.From == 1 && e.To == 0 }) {
		t.Fatal("bends back")
	}
	if rows[3].Node != 0 || rows[3].Width != 1 {
		t.Fatal("closes")
	}
}

// longRun has m merge f, which sits below a long run of mainline commits.
func longRun() []git.Commit {
	run := longEdge + 10
	commits := []git.Commit{commit("m", "b0", "f")}
	for i := range run {
		parent := "a"
		if i+1 < run {
			parent = fmt.Sprintf("b%d", i+1)
		}
		commits = append(commits, commit(fmt.Sprintf("b%d", i), parent))
	}
	return append(commits, commit("f", "a"), commit("a"))
}

func TestLongEdgeIsCutIntoArrowsAtBothEnds(t *testing.T) {
	commits := longRun()
	rows := Layout(commits, "")
	f := len(commits) - 2
	// The edge leaves m as an arrow...
	ix := slices.IndexFunc(rows[0].Arrows, func(a Arrow) bool { return a.Half == Bottom })
	if ix < 0 {
		t.Fatal("an arrow down")
	}
	down := rows[0].Arrows[ix]
	if down.Child != 0 || down.Parent != 1 || down.OtherEnd != f {
		t.Fatalf("%+v", down)
	}
	// ...takes no lane in between...
	for _, row := range rows[1:f] {
		if row.Width != 1 || len(row.Arrows) > 0 {
			t.Fatalf("%+v", row)
		}
	}
	// ...and arrives at f as one.
	ix = slices.IndexFunc(rows[f].Arrows, func(a Arrow) bool { return a.Half == Top })
	if ix < 0 {
		t.Fatal("an arrow up")
	}
	up := rows[f].Arrows[ix]
	if up.Child != 0 || up.Parent != 1 || up.OtherEnd != 0 || up.Color != down.Color {
		t.Fatalf("%+v", up)
	}
	for ix, row := range rows {
		if hasCrossing(&row) {
			t.Fatalf("row %d crosses", ix)
		}
	}
}

func TestEdgesOfTheCommitKeptWholeAreNotCut(t *testing.T) {
	commits := longRun()
	f := len(commits) - 2
	for _, whole := range []string{"m", "f"} {
		rows := Layout(commits, whole)
		for _, row := range rows {
			if len(row.Arrows) > 0 {
				t.Fatalf("kept %s", whole)
			}
		}
		for _, row := range rows[1:f] {
			if row.Width != 2 {
				t.Fatalf("kept %s", whole)
			}
		}
	}
}

// hasCrossing reports whether two edges in the same half of a row cross,
// swapping sides.
func hasCrossing(row *Row) bool {
	edges := row.Edges()
	for _, a := range edges {
		for _, b := range edges {
			if a.Half == b.Half && (a.From-b.From)*(a.To-b.To) < 0 {
				return true
			}
		}
	}
	return false
}

func TestMergeDoesNotCrossABendingLane(t *testing.T) {
	// M's first parent A is already expected right of M's lane, and its
	// second parent B needs a new lane.
	rows := Layout([]git.Commit{commit("y", "m"), commit("x", "a"), commit("m", "a", "b"), commit("b", "a"), commit("a")}, "")
	for ix, row := range rows {
		if hasCrossing(&row) {
			t.Fatalf("row %d crosses: %v", ix, row.Edges())
		}
	}
	if rows[4].Node != 0 {
		t.Fatal("a in lane 0")
	}
}
