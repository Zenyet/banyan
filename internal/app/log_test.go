package app

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
)

// A search over a history longer than a page leaves the row that offers to
// search every message, which is not a commit: it has no commit to key or
// name it by, and searching must not take the app down over it.
func TestSearchManyCommitsBuildsTheOfferRow(t *testing.T) {
	ws := branchesWorkspace(t)
	lv := ws.log
	lv.commits = make([]git.Commit, 6000)
	lv.rows = make([]int, len(lv.commits))
	for i := range lv.commits {
		lv.commits[i] = git.Commit{
			ID:      fmt.Sprintf("%040x", i),
			Subject: fmt.Sprintf("Change number %d", i),
			Author:  "Someone",
		}
		lv.rows[i] = i
	}
	lv.limit = len(lv.commits)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			lv.toolbar(c)
			lv.commitList(c)
		})
	}, 1200, 700)
	tt.Frame()
	// Matching none of the loaded commits leaves only the offer to search
	// the whole history.
	lv.focusSearch()
	tt.Frame()
	tt.Type("no commit has this")
	tt.Frame()
	if !tt.HasText("Searched the first 6000 commits") {
		t.Fatalf("no offer to search every message: %q", tt.Texts())
	}
	// Back to every commit, and to a query that matches some.
	for range "no commit has this" {
		tt.Key(0, ui.KeyBackspace)
	}
	tt.Frame()
	if !tt.HasText("Change number 0") {
		t.Fatalf("the search did not clear: %q", tt.Texts())
	}
	tt.Type("Change number 42")
	tt.Frame()
	if !tt.HasText("Change number 42") {
		t.Fatalf("the search found nothing: %q", tt.Texts())
	}
}

// The search matches subjects, authors, refs and hash prefixes, ignores
// case, narrows its rows as the query grows, and looks again over a new
// log rather than narrow the old rows.
func TestApplyFilterMatchesAndRescans(t *testing.T) {
	lv := &LogView{query: "parser"}
	lv.commits = []git.Commit{
		{ID: "aaaa1111", Subject: "Fix the Parser", Author: "Ann"},
		{ID: "bbbb2222", Subject: "Tidy up", Author: "Bob Parser"},
		{ID: "cccc3333", Subject: "Release", Author: "Ann", Refs: []git.RefLabel{{Name: "origin/Parser-fix"}}},
		{ID: "dddd4444", Subject: "Docs", Author: "Cid"},
	}
	lv.logGen = 1
	if rows := rowsOf(lv); len(rows) != 3 {
		t.Fatalf("query parser shows rows %v, want three", rows)
	}
	lv.query = "PARSER-F"
	if rows := rowsOf(lv); len(rows) != 1 || rows[0] != 2 {
		t.Fatalf("query PARSER-F shows rows %v, want the ref", rows)
	}
	lv.query = "cccc"
	if rows := rowsOf(lv); len(rows) != 1 || rows[0] != 2 {
		t.Fatalf("hash prefix shows rows %v, want the third", rows)
	}
	// The same query over another log shows its commits, not the rows of
	// the old one.
	lv.commits = []git.Commit{{ID: "eeee5555", Subject: "Docs", Author: "Cid"}}
	lv.logGen++
	lv.query = "parser"
	if rows := rowsOf(lv); len(rows) != 0 {
		t.Fatalf("the new log shows rows %v, want none", rows)
	}
}

func rowsOf(lv *LogView) []int {
	lv.applyFilter()
	return lv.rows
}
