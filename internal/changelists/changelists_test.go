package changelists

import (
	"reflect"
	"testing"
)

func TestNewChangesGoToTheActiveList(t *testing.T) {
	lists := New()
	lists.Create("Refactor")
	lists.Sync([]string{"a.rs"})
	lists.SetActive("Refactor")
	lists.Sync([]string{"a.rs", "b.rs"})
	if lists.ListOf("a.rs") != Default || lists.ListOf("b.rs") != "Refactor" {
		t.Fatal("new changes go to the active list")
	}
	// Committed files are forgotten; when they change again they start over.
	lists.Sync([]string{"b.rs"})
	lists.SetActive(Default)
	lists.Sync([]string{"a.rs", "b.rs"})
	if lists.ListOf("a.rs") != Default || lists.ListOf("b.rs") != "Refactor" {
		t.Fatal("committed files start over")
	}
}

func TestDeletingAListMovesItsFiles(t *testing.T) {
	lists := New()
	lists.MoveTo([]string{"a.rs"}, "Debug")
	lists.SetActive("Debug")
	lists.Delete("Debug")
	if !reflect.DeepEqual(lists.Names(), []string{Default}) || lists.Active() != Default || lists.ListOf("a.rs") != Default {
		t.Fatal("files move to the active list")
	}
	if lists.Delete(Default) == nil {
		t.Fatal("the last list stays")
	}
}

func TestRenamingKeepsFilesAndTheActiveMark(t *testing.T) {
	lists := New()
	lists.Sync([]string{"a.rs"})
	lists.Rename(Default, "Feature")
	if lists.Active() != "Feature" || lists.ListOf("a.rs") != "Feature" {
		t.Fatal("rename")
	}
	lists.Create("Other")
	if lists.Rename("Other", "Feature") == nil || lists.Create(" ") == nil {
		t.Fatal("names must be new and not blank")
	}
}

func TestRoundTripsThroughText(t *testing.T) {
	lists := New()
	lists.MoveTo([]string{"src/a b.rs", "c.rs"}, "Fix login")
	lists.SetActive("Fix login")
	if got := parse(lists.text()); !reflect.DeepEqual(got, lists) {
		t.Fatalf("got %+v want %+v", got, lists)
	}
	// Files of lists that no longer exist fall back to the active list.
	parsed := parse("list=A\nactive=Gone\nfile=Gone\tx.rs\n")
	if parsed.Active() != "A" || parsed.ListOf("x.rs") != "A" {
		t.Fatal("fallback")
	}
}
