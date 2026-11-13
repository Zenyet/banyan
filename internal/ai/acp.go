package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Zenyet/banyan/internal/i18n"
)

// The Agent Client Protocol, v2 and v1, as far as commit messages need it:
// initialize, session/new, session/prompt and the updates that stream the
// answer. v1 also lets the agent read files and run commands, through the
// client; v2 leaves that to the agent's own tools.
type acpClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *tailBuffer

	root         string
	allowTools   bool
	version      int
	sessionID    string
	clientVer    string
	agentName    string
	agentVersion string

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan acpEnvelope
	nextID  atomic.Int64
	exitErr error
	closed  chan struct{}
	done    *sync.Once

	textMu sync.Mutex
	texts  map[string]string
	order  []string
	idle   chan acpIdle
	// sawBusy is set once the agent reports foreground work for the turn;
	// an idle state before that is the session's opening snapshot.
	sawBusy atomic.Bool
	// turnActive is set while a prompt is being sent and answered, so that
	// state updates from before it are not taken for its end.
	turnActive atomic.Bool

	termsMu sync.Mutex
	terms   map[string]*acpTerminal
	termSeq atomic.Int64

	closeOnce sync.Once
}

type acpEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

type acpIdle struct {
	StopReason string
	Error      string
}

type acpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func startACP(ctx context.Context, cfg Config, root string) (*acpClient, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", cfg.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", cfg.Command)
	}
	cmd.Dir = root
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &tailBuffer{limit: 8 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("Could not start the ACP agent"), err)
	}
	c := &acpClient{
		cmd:        cmd,
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
		root:       root,
		allowTools: cfg.AllowTools,
		clientVer:  cfg.ClientVersion,
		pending:    map[string]chan acpEnvelope{},
		closed:     make(chan struct{}),
		done:       &sync.Once{},
		texts:      map[string]string{},
		idle:       make(chan acpIdle, 1),
		terms:      map[string]*acpTerminal{},
	}
	go c.read()
	go func() {
		err := cmd.Wait()
		if err != nil && !errors.Is(err, context.Canceled) {
			c.fail(fmt.Errorf("%s: %w", c.exitMessage(), err))
		} else {
			c.fail(errors.New(c.exitMessage()))
		}
	}()
	return c, nil
}

// initialize negotiates the ACP version. both is false for the retry that
// older agents need, which know only v1's parameter names.
func (c *acpClient) initialize(ctx context.Context, both bool) (int, error) {
	capabilities := map[string]any{
		"fs":       map[string]bool{"readTextFile": true, "writeTextFile": false},
		"terminal": c.allowTools,
	}
	params := map[string]any{
		"protocolVersion":    2,
		"clientCapabilities": capabilities,
		"clientInfo": map[string]string{
			"name": "banyan", "title": "Banyan", "version": c.clientVer,
		},
	}
	if both {
		params["capabilities"] = map[string]any{}
		params["info"] = map[string]string{"name": "banyan", "title": "Banyan", "version": c.clientVer}
	} else {
		params["protocolVersion"] = 1
	}
	raw, err := c.request(ctx, "initialize", params)
	if err != nil {
		return 0, err
	}
	var result struct {
		ProtocolVersion int `json:"protocolVersion"`
		AgentInfo       struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"agentInfo"`
		Info struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, err
	}
	if result.ProtocolVersion < 1 || result.ProtocolVersion > 2 {
		return 0, fmt.Errorf("%s: %d", i18n.T("The agent speaks an ACP version Banyan does not"), result.ProtocolVersion)
	}
	c.version = result.ProtocolVersion
	c.agentName, c.agentVersion = result.AgentInfo.Title, result.AgentInfo.Version
	if c.agentName == "" {
		c.agentName = result.AgentInfo.Name
	}
	if c.agentName == "" {
		c.agentName, c.agentVersion = result.Info.Title, result.Info.Version
	}
	if c.agentName == "" {
		c.agentName = result.Info.Name
	}
	return result.ProtocolVersion, nil
}

func (c *acpClient) newSession(ctx context.Context) (string, error) {
	raw, err := c.request(ctx, "session/new", map[string]any{
		"cwd":        c.root,
		"mcpServers": []any{},
	})
	if err != nil {
		return "", err
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	if result.SessionID == "" {
		return "", errors.New(i18n.T("The agent made no session"))
	}
	c.sessionID = result.SessionID
	return result.SessionID, nil
}

// promptTurn sends the prompt and waits for the message. ACP v1 answers
// when the turn ends; v2 answers once the prompt is accepted and reports
// the end with an idle state update.
func (c *acpClient) promptTurn(ctx context.Context, text string) (string, error) {
	c.resetText()
	c.drainIdle()
	c.sawBusy.Store(false)
	c.turnActive.Store(true)
	defer c.turnActive.Store(false)
	params := map[string]any{
		"sessionId": c.sessionID,
		"prompt": []map[string]string{
			{"type": "text", "text": text},
		},
	}
	raw, err := c.request(ctx, "session/prompt", params)
	if err != nil {
		return "", err
	}
	if c.version >= 2 {
		select {
		case <-ctx.Done():
			c.notify("session/cancel", map[string]string{"sessionId": c.sessionID})
			return "", ctx.Err()
		case idle := <-c.idle:
			switch idle.StopReason {
			case "cancelled":
				return "", context.Canceled
			case "error", "refusal":
				if idle.Error != "" {
					return "", errors.New(idle.Error)
				}
				return "", errors.New(i18n.T("The agent stopped without a message"))
			}
		}
	} else {
		var result struct {
			StopReason string `json:"stopReason"`
		}
		_ = json.Unmarshal(raw, &result)
		switch result.StopReason {
		case "cancelled":
			return "", context.Canceled
		case "refusal":
			return "", errors.New(i18n.T("The agent declined to write the message"))
		}
	}
	return c.collectedText(), nil
}

func (c *acpClient) read() {
	reader := bufio.NewReaderSize(c.stdout, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			c.handle(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.fail(errors.New(c.exitMessage()))
			} else {
				c.fail(err)
			}
			return
		}
	}
}

func (c *acpClient) handle(line []byte) {
	var env acpEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(line), &env); err != nil {
		return
	}
	if env.Method != "" {
		if hasID(env.ID) {
			go c.serve(env)
		} else {
			c.notification(env)
		}
		return
	}
	if !hasID(env.ID) {
		return
	}
	key := string(env.ID)
	c.mu.Lock()
	wait := c.pending[key]
	c.mu.Unlock()
	if wait != nil {
		select {
		case wait <- env:
		default:
		}
	}
}

func hasID(id json.RawMessage) bool {
	return len(id) > 0 && string(bytes.TrimSpace(id)) != "null"
}

func (c *acpClient) notification(env acpEnvelope) {
	if env.Method != "session/update" {
		return
	}
	var update struct {
		Update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			MessageID     string          `json:"messageId"`
			Content       json.RawMessage `json:"content"`
			State         string          `json:"state"`
			StopReason    string          `json:"stopReason"`
			Error         json.RawMessage `json:"error"`
		} `json:"update"`
	}
	if err := json.Unmarshal(env.Params, &update); err != nil {
		return
	}
	u := update.Update
	switch u.SessionUpdate {
	case "agent_message_chunk":
		var content acpContent
		if json.Unmarshal(u.Content, &content) == nil && content.Type == "text" {
			c.addText(u.MessageID, content.Text, false)
		}
	case "agent_message":
		var content []acpContent
		if json.Unmarshal(u.Content, &content) == nil {
			var b strings.Builder
			for _, block := range content {
				if block.Type == "text" {
					b.WriteString(block.Text)
				}
			}
			c.addText(u.MessageID, b.String(), true)
		}
	case "state_update":
		if !c.turnActive.Load() {
			return
		}
		if u.State != "idle" {
			c.sawBusy.Store(true)
			return
		}
		// An idle state before any work is the session's opening snapshot,
		// not the end of this prompt.
		if !c.sawBusy.Load() {
			return
		}
		idle := acpIdle{StopReason: u.StopReason, Error: errorText(u.Error)}
		select {
		case c.idle <- idle:
		default:
		}
	}
}

// errorText is an update's error, a string or an object with a message.
func errorText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var object struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &object) == nil {
		return object.Message
	}
	return string(raw)
}

func (c *acpClient) addText(id, text string, replace bool) {
	if text == "" {
		return
	}
	if id == "" {
		id = "agent"
	}
	c.textMu.Lock()
	defer c.textMu.Unlock()
	if _, ok := c.texts[id]; !ok {
		c.order = append(c.order, id)
	}
	if replace {
		c.texts[id] = text
	} else {
		c.texts[id] += text
	}
}

func (c *acpClient) resetText() {
	c.textMu.Lock()
	c.texts = map[string]string{}
	c.order = nil
	c.textMu.Unlock()
}

func (c *acpClient) collectedText() string {
	c.textMu.Lock()
	defer c.textMu.Unlock()
	var parts []string
	for _, id := range c.order {
		if text := strings.TrimSpace(c.texts[id]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func (c *acpClient) drainIdle() {
	select {
	case <-c.idle:
	default:
	}
}

func (c *acpClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	key := strconv.FormatInt(id, 10)
	wait := make(chan acpEnvelope, 1)
	c.mu.Lock()
	c.pending[key] = wait
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
	}()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		if c.sessionID != "" {
			c.notify("session/cancel", map[string]string{"sessionId": c.sessionID})
		}
		return nil, ctx.Err()
	case <-c.closed:
		return nil, c.failure()
	case env := <-wait:
		if env.Error != nil {
			return nil, fmt.Errorf("%s (ACP %d)", env.Error.Message, env.Error.Code)
		}
		return env.Result, nil
	}
}

func (c *acpClient) notify(method string, params any) {
	_ = c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *acpClient) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(data)
	return err
}

func (c *acpClient) reply(id json.RawMessage, result any) {
	_ = c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *acpClient) replyError(id json.RawMessage, code int, message string) {
	_ = c.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (c *acpClient) serve(env acpEnvelope) {
	switch env.Method {
	case "fs/read_text_file":
		result, err := c.readTextFile(env.Params)
		if err != nil {
			c.replyError(env.ID, -32602, err.Error())
			return
		}
		c.reply(env.ID, result)
	case "session/request_permission":
		c.reply(env.ID, c.permission(env.Params))
	case "terminal/create", "terminal/output", "terminal/wait_for_exit", "terminal/kill", "terminal/release":
		if !c.allowTools {
			c.replyError(env.ID, -32601, i18n.T("The client does not run commands for the agent"))
			return
		}
		result, err := c.terminal(env.Method, env.Params)
		if err != nil {
			c.replyError(env.ID, -32603, err.Error())
			return
		}
		c.reply(env.ID, result)
	default:
		c.replyError(env.ID, -32601, i18n.T("Method not supported"))
	}
}

func (c *acpClient) readTextFile(raw json.RawMessage) (any, error) {
	var p struct {
		Path  string `json:"path"`
		Line  *int   `json:"line"`
		Limit *int   `json:"limit"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(p.Path)
	if err != nil {
		return nil, err
	}
	if !within(c.root, abs) {
		return nil, errors.New(i18n.T("The agent asked for a file outside the repository"))
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	content := string(data)
	if p.Line != nil {
		lines := strings.SplitAfter(content, "\n")
		start := max(*p.Line-1, 0)
		if start > len(lines) {
			start = len(lines)
		}
		end := len(lines)
		if p.Limit != nil && *p.Limit > 0 && start+*p.Limit < end {
			end = start + *p.Limit
		}
		content = strings.Join(lines[start:end], "")
	}
	if len(content) > 2<<20 {
		content = content[:2<<20]
	}
	return map[string]string{"content": content}, nil
}

func (c *acpClient) permission(raw json.RawMessage) any {
	var p struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if c.allowTools && json.Unmarshal(raw, &p) == nil {
		// Once is enough: Banyan asks the agent again only when it wants to
		// do something else.
		for _, kind := range []string{"allow_once", "allow_always"} {
			for _, option := range p.Options {
				if option.Kind == kind {
					return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": option.OptionID}}
				}
			}
		}
	}
	return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
}

// within reports whether path is root or inside it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (c *acpClient) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exitErr == nil {
		return errors.New(i18n.T("The ACP agent stopped"))
	}
	return c.exitErr
}

func (c *acpClient) fail(err error) {
	c.mu.Lock()
	if c.exitErr == nil {
		c.exitErr = err
	}
	c.mu.Unlock()
	c.done.Do(func() { close(c.closed) })
}

func (c *acpClient) exitMessage() string {
	tail := strings.TrimSpace(c.stderr.String())
	message := i18n.T("The ACP agent stopped")
	if tail != "" {
		message += ": " + tail
	}
	return message
}

func (c *acpClient) close() {
	c.closeOnce.Do(func() {
		c.termsMu.Lock()
		for id, term := range c.terms {
			term.kill()
			delete(c.terms, id)
		}
		c.termsMu.Unlock()
		_ = c.stdin.Close()
		select {
		case <-c.closed:
		case <-time.After(2 * time.Second):
			killProcessGroup(c.cmd)
		}
		select {
		case <-c.closed:
		case <-time.After(2 * time.Second):
			if c.cmd.Process != nil {
				_ = c.cmd.Process.Kill()
			}
		}
	})
}

// tailBuffer keeps the last bytes written to it, for an error's words.
type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = b.data[len(b.data)-b.limit:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// openACP starts the configured agent in root and negotiates the protocol.
// An agent of before v2 may know only v1's parameter names, so a failed
// negotiation is tried again as v1.
func openACP(ctx context.Context, cfg Config, root string) (*acpClient, int, error) {
	client, err := startACP(ctx, cfg, root)
	if err != nil {
		return nil, 0, err
	}
	version, err := client.initialize(ctx, true)
	if err == nil {
		return client, version, nil
	}
	client.close()
	client, err = startACP(ctx, cfg, root)
	if err != nil {
		return nil, 0, err
	}
	version, err = client.initialize(ctx, false)
	if err != nil {
		client.close()
		return nil, 0, err
	}
	return client, version, nil
}

// generateACP runs the configured agent for one message, in the
// repository, and closes it again.
func generateACP(ctx context.Context, cfg Config, req Request, prompt string) (string, error) {
	client, version, err := openACP(ctx, cfg, req.Root)
	if err != nil {
		return "", err
	}
	defer client.close()
	if version < 1 {
		return "", errors.New(i18n.T("The agent did not say which ACP version it speaks"))
	}
	if _, err := client.newSession(ctx); err != nil {
		return "", err
	}
	return client.promptTurn(ctx, prompt)
}

// checkACP starts the agent in a temporary folder and asks for a word, so
// that its command, login and model are all tested.
func checkACP(ctx context.Context, cfg Config) (string, error) {
	// A test needs no tools, and runs none even when the setting allows
	// them.
	cfg.AllowTools = false
	dir, err := os.MkdirTemp("", "banyan-acp-check-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	client, version, err := openACP(ctx, cfg, dir)
	if err != nil {
		return "", err
	}
	defer client.close()
	if version < 1 {
		return "", errors.New(i18n.T("The agent did not say which ACP version it speaks"))
	}
	if _, err := client.newSession(ctx); err != nil {
		return "", err
	}
	text, err := client.promptTurn(ctx, "Reply with the single word OK. Do not use tools, run commands or change files.")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New(i18n.T("The agent returned no message"))
	}
	detail := strings.TrimSpace(client.agentName)
	if detail == "" {
		detail = i18n.T("ACP agent")
	}
	if client.agentVersion != "" {
		detail += " " + client.agentVersion
	}
	return detail, nil
}
