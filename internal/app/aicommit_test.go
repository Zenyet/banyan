package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/patch"
	"github.com/Zenyet/banyan/internal/settings"
)

// The provider is shown exactly the checked changes, including a new file.
func TestAIRequestShowsTheCheckedChanges(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	write("a.txt", "one\n")
	run("add", "a.txt")
	run("commit", "-q", "-m", "Base commit")
	write("a.txt", "one\ntwo\n")
	write("new.txt", "brand new\n")
	write("package-lock.json", "{\"secret\": \"lock-secret\"}\n")
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	status, err := repo.Status()
	if err != nil {
		t.Fatal(err)
	}
	snap := aiSnapshot{root: dir, branch: "main", entries: status, excluded: map[string][]patch.ChangedLine{}}
	req, err := snap.request(repo, "en")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.Diff, "+two") || !strings.Contains(req.Diff, "+brand new") {
		t.Errorf("diff misses the changes:\n%s", req.Diff)
	}
	if strings.Contains(req.Diff, "lock-secret") {
		t.Errorf("lock file's diff was sent:\n%s", req.Diff)
	}
	files := strings.Join(req.Files, "\n")
	if len(req.Files) != 3 || !strings.Contains(files, "new.txt") || !strings.Contains(files, "package-lock.json") {
		t.Errorf("files = %v", req.Files)
	}
	if len(req.Recent) == 0 || req.Recent[0] != "Base commit" {
		t.Errorf("recent = %v", req.Recent)
	}
	if req.Language != "en" {
		t.Errorf("language = %q", req.Language)
	}
}

func TestNoisyDiff(t *testing.T) {
	for _, file := range []string{"package-lock.json", "web/yarn.lock", "dist/app.min.js", "a/b.map", "Cargo.lock"} {
		if !noisyDiff(file) {
			t.Errorf("%q should be left out", file)
		}
	}
	for _, file := range []string{"main.go", "README.md", "lock.txt", "src/map.go"} {
		if noisyDiff(file) {
			t.Errorf("%q should be sent", file)
		}
	}
}

// Every kind of provider draws its page, with the fields it needs.
func TestAISettingsPageRenders(t *testing.T) {
	for _, provider := range []string{"", "acp", "openai", "anthropic", "compatible"} {
		s := settings.Default()
		s.AIProvider = provider
		s.AIModel = "test-model"
		s.AIAPIKey = "test-key"
		sw := &settingsWindow{
			aiCommand:      "agent-acp",
			aiBaseURL:      "http://localhost:11434/v1",
			aiModel:        "test-model",
			aiKey:          "test-key",
			aiInstructions: "Use Conventional Commits",
			aiTools:        true,
		}
		tt := ui.NewTester(func(c *ui.Context) { sw.ai(c, &s) }, 820, 700)
		if _, ok := tt.Find("Generate with"); !ok {
			t.Fatalf("%q: no provider choice\n%s", provider, strings.Join(tt.Texts(), "\n"))
		}
		if provider == "acp" {
			if _, ok := tt.Find("Agent command"); !ok {
				t.Fatalf("acp: no command field\n%s", strings.Join(tt.Texts(), "\n"))
			}
		}
		if provider == "openai" {
			if _, ok := tt.Find("API key"); !ok {
				t.Fatalf("openai: no key field\n%s", strings.Join(tt.Texts(), "\n"))
			}
			sw.aiTestCfg = sw.aiConfig(&s)
			sw.aiTestDetail = "OpenAI · test-model"
			tt = ui.NewTester(func(c *ui.Context) { sw.ai(c, &s) }, 820, 700)
			if !strings.Contains(strings.Join(tt.Texts(), "\n"), "Connected: OpenAI · test-model") {
				t.Fatalf("no connection success\n%s", strings.Join(tt.Texts(), "\n"))
			}
			sw.aiTestDetail = ""
			sw.aiTestErr = "invalid key"
			tt = ui.NewTester(func(c *ui.Context) { sw.ai(c, &s) }, 820, 700)
			if !strings.Contains(strings.Join(tt.Texts(), "\n"), "invalid key") {
				t.Fatalf("no connection error\n%s", strings.Join(tt.Texts(), "\n"))
			}
		}
		if provider != "" {
			for _, label := range []string{"Length", "Language", "Style", "Test Connection"} {
				if _, ok := tt.Find(label); !ok {
					t.Fatalf("%q: no %q\n%s", provider, label, strings.Join(tt.Texts(), "\n"))
				}
			}
		}
	}
}

// The commit box offers generation even before a provider is set up.
func TestCommitBoxOffersGeneration(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("one\n")
	run("add", ".")
	run("commit", "-q", "-m", "Base")
	write("one\ntwo\n")
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model.New(repo)
	defer m.Close()
	for deadline := time.Now().Add(5 * time.Second); len(m.Status) == 0 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if len(m.Status) == 0 {
		t.Fatal("no status")
	}
	ws := newWorkspace(&Shell{}, m)
	tt := ui.NewTester(func(c *ui.Context) { ws.changes.commitBox(c) }, 900, 300)
	texts := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(texts, "Generate Commit Message") {
		t.Fatalf("no generate button:\n%s", texts)
	}
	// While writing, the button itself is the spinner, and clicking it
	// cancels.
	ws.changes.generating = true
	tt = ui.NewTester(func(c *ui.Context) { ws.changes.commitBox(c) }, 900, 300)
	texts = strings.Join(tt.Texts(), "\n")
	if strings.Contains(texts, "Writing a commit message") || !strings.Contains(texts, "Cancel generating") {
		t.Fatalf("generating state:\n%s", texts)
	}
}
