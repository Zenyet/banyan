package i18n

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestFillsPlaceholdersInOrderOrByNumber(t *testing.T) {
	cases := []struct {
		template string
		args     []any
		want     string
	}{
		{"Merge '{}' into '{}'", []any{"a", "b"}, "Merge 'a' into 'b'"},
		{"将“{0}”合并到“{1}”", []any{"a", "b"}, "将“a”合并到“b”"},
		{"“{1}”变基到“{0}”", []any{"a", "b"}, "“b”变基到“a”"},
		{"{x} stays", []any{1}, "{x} stays"},
	}
	for _, c := range cases {
		if got := fill(c.template, c.args); got != c.want {
			t.Errorf("fill(%q) = %q, want %q", c.template, got, c.want)
		}
	}
}

// Every i18n.T and i18n.Tf string in the sources has a Chinese translation
// with the same placeholders.
func TestEveryStringIsTranslated(t *testing.T) {
	call := regexp.MustCompile(`i18n\.Tf?\(("(?:[^"\\]|\\.)*")`)
	var missing []string
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range call.FindAllStringSubmatch(string(source), -1) {
			text, err := strconv.Unquote(m[1])
			if err != nil {
				continue
			}
			translated, ok := zh[text]
			switch {
			case !ok:
				missing = append(missing, path+": "+text)
			case strings.Count(translated, "{") != strings.Count(text, "{"):
				missing = append(missing, "placeholders differ: "+text+" → "+translated)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Errorf("untranslated:\n%s", strings.Join(missing, "\n"))
	}
}
