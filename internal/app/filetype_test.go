package app

import "testing"

func TestFileIconNames(t *testing.T) {
	for path, want := range map[string]string{
		"main.go":                "go",
		"internal/app/go.mod":    "go-mod",
		"src/App.TSX":            "react-ts",
		"types/index.d.ts":       "typescript-def",
		"Dockerfile.dev":         "docker",
		".env.local":             "tune",
		"README.md":              "readme",
		"docs/guide.md":          "markdown",
		"web/vite.config.ts":     "vite",
		"assets/logo.png":        "image",
		"Makefile":               "makefile",
		"notes":                  "document",
		"archive.tar.gz":         "zip",
		"scripts/bundle.sh":      "console",
		"packaging/banyan.plist": "xml",
	} {
		if got := fileIconName(path); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
}

// Every icon a file can get is there: a missing one would panic as the
// file shows.
func TestEveryFileIconExists(t *testing.T) {
	names := map[string]bool{"document": true, "typescript-def": true}
	for _, icon := range fileNameIcons {
		names[icon] = true
	}
	for _, f := range fileNamePrefixIcons {
		names[f.icon] = true
	}
	for _, icon := range fileExtIcons {
		names[icon] = true
	}
	for name := range names {
		if _, err := fileIconFiles.ReadFile("icons/files/" + name + ".svg"); err != nil {
			t.Errorf("no icon %s", name)
		}
	}
}
