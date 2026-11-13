package ai

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestCleanKeepsTheMessage(t *testing.T) {
	cases := map[string]string{
		"feat: add a thing":                                                "feat: add a thing",
		"Here is the commit message:\n\nfix: a bug":                        "fix: a bug",
		"```\nfeat: fenced\n\nBody.\n```":                                  "feat: fenced\n\nBody.",
		"```text\nfeat: language\n```":                                     "feat: language",
		"I looked at the diff.\n```\nfix: last block\n```\nHope it helps.": "fix: last block",
		"\"feat: quoted\"":                                                 "feat: quoted",
		"Refactor\n\nWithout a fence.":                                     "Refactor\n\nWithout a fence.",
	}
	for input, want := range cases {
		if got := Clean(input); got != want {
			t.Errorf("Clean(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPromptDescribesTheCommit(t *testing.T) {
	cfg := Config{Instructions: "Use Conventional Commits"}
	req := Request{
		Root:     "/tmp/banyan",
		Branch:   "feature/ai",
		Files:    []string{"M internal/app/changes.go"},
		Diff:     "diff --git a/x b/x\n+hello\n",
		Recent:   []string{"fix: old bug"},
		Language: "zh",
	}
	got := prompt(cfg, req)
	for _, want := range []string{
		"Repository: banyan",
		"Current branch: feature/ai",
		"M internal/app/changes.go",
		"fix: old bug",
		"+hello",
		"Simplified Chinese",
		"Use Conventional Commits",
		"only the commit message",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
}

func TestSubjectOnly(t *testing.T) {
	cfg := Config{SubjectOnly: true}
	got := prompt(cfg, Request{Root: "/tmp/banyan", Diff: "+a\n"})
	if !strings.Contains(got, "Do not add a body") {
		t.Errorf("prompt does not keep it to one line:\n%s", got)
	}
	if strings.Contains(got, "Leave a blank line") {
		t.Errorf("prompt asks for a body:\n%s", got)
	}
	withBody := prompt(Config{}, Request{Root: "/tmp/banyan", Diff: "+a\n"})
	if !strings.Contains(withBody, "Leave a blank line") {
		t.Errorf("prompt cannot ask for a body:\n%s", withBody)
	}
	if got := firstLine("\nfeat: short\n\nA body.\n"); got != "feat: short" {
		t.Errorf("firstLine = %q", got)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{}, "Settings"},
		{Config{Provider: ProviderACP}, "command"},
		{Config{Provider: ProviderOpenAI, APIKey: "k"}, "model"},
		{Config{Provider: ProviderOpenAI, Model: "m"}, "key"},
		{Config{Provider: ProviderCompatible, Model: "m"}, "URL"},
		{Config{Provider: ProviderCompatible, BaseURL: "http://localhost", Model: "m"}, ""},
	}
	for _, c := range cases {
		err := c.cfg.Validate()
		if c.want == "" {
			if err != nil {
				t.Errorf("%+v: %v", c.cfg, err)
			}
			continue
		}
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.want)) {
			t.Errorf("%+v: %v, want %q", c.cfg, err, c.want)
		}
	}
}

func TestWithin(t *testing.T) {
	root := "/repo"
	for path, want := range map[string]bool{
		"/repo/a":       true,
		"/repo/a/b":     true,
		"/repo":         true,
		"/repository/a": false,
		"/repo/../etc":  false,
		"/etc/passwd":   false,
	} {
		if got := within(root, path); got != want {
			t.Errorf("within(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestACP runs a fake agent to check the protocol: v2's answer comes with
// an idle state update after the prompt is accepted.
func TestACP(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(root+"/a.txt", []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BANYAN_FAKE_ACP", "1")
	t.Setenv("BANYAN_FAKE_ACP_V1", "")
	cfg := Config{Provider: ProviderACP, Command: shellQuote(testBinary(t)), AllowTools: true, ClientVersion: "test"}
	got, err := Generate(context.Background(), cfg, Request{Root: root, Branch: "main", Diff: "+hello\n"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "feat: answer from the fake agent\n\nIt read a.txt."; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// TestACPV1 checks the older flow, where the prompt's response ends the
// turn.
func TestACPV1(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(root+"/a.txt", []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BANYAN_FAKE_ACP", "1")
	t.Setenv("BANYAN_FAKE_ACP_V1", "1")
	cfg := Config{Provider: ProviderACP, Command: shellQuote(testBinary(t)), AllowTools: true}
	got, err := Generate(context.Background(), cfg, Request{Root: root, Branch: "main", Diff: "+hello\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "feat: answer from the fake agent") {
		t.Errorf("message = %q", got)
	}
}

// TestACPFallsBackToV1 covers an older agent that refuses to negotiate v2.
func TestACPFallsBackToV1(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BANYAN_FAKE_ACP", "1")
	t.Setenv("BANYAN_FAKE_ACP_V1", "1")
	t.Setenv("BANYAN_FAKE_ACP_STRICT", "1")
	cfg := Config{Provider: ProviderACP, Command: shellQuote(testBinary(t))}
	got, err := Generate(context.Background(), cfg, Request{Root: root, Diff: "+hello\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "feat: answer from the fake agent") {
		t.Errorf("message = %q", got)
	}
}

func testBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func shellQuote(s string) string {
	if runtime.GOOS == "windows" {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestMain turns this test binary into a fake ACP agent when the tests ask
// it to, before the testing package writes anything to stdout.
func TestMain(m *testing.M) {
	if os.Getenv("BANYAN_FAKE_ACP") == "1" {
		fakeAgent()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeAgent speaks enough ACP to answer a prompt, in v1 or v2.
func fakeAgent() {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	v1 := os.Getenv("BANYAN_FAKE_ACP_V1") == "1"
	session := ""
	for {
		var env struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := dec.Decode(&env); err != nil {
			return
		}
		switch env.Method {
		case "initialize":
			var params struct {
				ProtocolVersion int `json:"protocolVersion"`
			}
			_ = json.Unmarshal(env.Params, &params)
			if os.Getenv("BANYAN_FAKE_ACP_STRICT") == "1" && params.ProtocolVersion >= 2 {
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": env.ID, "error": map[string]any{"code": -32022, "message": "Unsupported protocol version"}})
				continue
			}
			version := 2
			if v1 {
				version = 1
			}
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": env.ID, "result": map[string]any{"protocolVersion": version}})
		case "session/new":
			session = "fake-session"
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": env.ID, "result": map[string]any{"sessionId": session}})
		case "session/prompt":
			if v1 {
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
					"sessionId": session,
					"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "feat: answer from the fake agent\n\nIt read a.txt."}},
				}})
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": env.ID, "result": map[string]any{"stopReason": "end_turn"}})
				continue
			}
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": env.ID, "result": map[string]any{"messageId": "user-message"}})
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": session,
				"update":    map[string]any{"sessionUpdate": "state_update", "state": "running"},
			}})
			for _, text := range []string{"feat: answer from the fake agent\n\n", "It read a.txt."} {
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
					"sessionId": session,
					"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "agent-message", "content": map[string]any{"type": "text", "text": text}},
				}})
			}
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": session,
				"update":    map[string]any{"sessionUpdate": "state_update", "state": "idle", "stopReason": "end_turn"},
			}})
		}
	}
}

// An agent may read the repository's files, and no others.
func TestACPReadTextFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(root+"/a.txt", []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &acpClient{root: root}
	params, _ := json.Marshal(map[string]any{"path": root + "/a.txt", "line": 2, "limit": 1})
	result, err := c.readTextFile(params)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]string)["content"]; got != "two\n" {
		t.Errorf("content = %q", got)
	}
	params, _ = json.Marshal(map[string]any{"path": root + "/../outside.txt"})
	if _, err := c.readTextFile(params); err == nil {
		t.Error("read a file outside the repository")
	}
}

func TestStylePrompts(t *testing.T) {
	cases := map[Style]string{
		StyleConventional: "Conventional Commits",
		StyleGitmoji:      "Gitmoji",
		StylePlain:        "without a type prefix or emoji",
	}
	for style, want := range cases {
		if got := prompt(Config{Style: style}, Request{Root: "/tmp/repo"}); !strings.Contains(got, want) {
			t.Errorf("%q prompt lacks %q:\n%s", style, want, got)
		}
	}
	if got := prompt(Config{Style: StyleRepository}, Request{Root: "/tmp/repo"}); strings.Contains(got, "Use Conventional Commits") {
		t.Errorf("repository style forces a convention:\n%s", got)
	}
}

// TestCheckACP uses the fake agent for the settings' Test Connection.
func TestCheckACP(t *testing.T) {
	t.Setenv("BANYAN_FAKE_ACP", "1")
	t.Setenv("BANYAN_FAKE_ACP_V1", "")
	cfg := Config{Provider: ProviderACP, Command: shellQuote(testBinary(t))}
	detail, err := Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if detail == "" {
		t.Error("no detail")
	}
}
