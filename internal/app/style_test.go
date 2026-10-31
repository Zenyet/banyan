package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

// A filter's pill opens the system's menu, and the item chosen applies.
func TestFilterButtonOpensItsMenu(t *testing.T) {
	filter := "All"
	tt := ui.NewTester(func(c *ui.Context) {
		filterButton(c, "Branch: "+filter).Menu(func(m *ui.Menu) {
			for _, name := range []string{"All", "main", "refactor"} {
				if m.Item(name).Checked(filter == name).Chosen() {
					filter = name
				}
			}
		})
	}, 400, 200)
	if err := tt.Click("Branch: All"); err != nil {
		t.Fatal(err)
	}
	if items := tt.Menu(); !slices.Equal(items, []string{"All", "main", "refactor"}) {
		t.Fatalf("the menu shows %q", items)
	}
	if err := tt.ChooseMenuItem("refactor"); err != nil {
		t.Fatal(err)
	}
	if filter != "refactor" || !tt.HasText("Branch: refactor") {
		t.Fatalf("the filter is %q, the pill says %q", filter, tt.Texts())
	}
}

// A setting's drop-down opens the system's menu of its options. Choosing
// one would save the settings, so the test stops at the menu.
func TestSettingChoiceOpensItsMenu(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		choice(c, []option{{"split", "Side by side"}, {"unified", "Unified"}}, "unified", nil)
	}, 400, 200)
	if err := tt.Click("Unified"); err != nil {
		t.Fatal(err)
	}
	if items := tt.Menu(); !slices.Equal(items, []string{"Side by side", "Unified"}) {
		t.Fatalf("the menu shows %q", items)
	}
}
