package ai

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/Zenyet/banyan/internal/i18n"
)

// acpTerminal is a command an ACP v1 agent runs through the client, as the
// protocol's terminal methods let it.
type acpTerminal struct {
	cmd   *exec.Cmd
	out   *tailBuffer
	done  chan struct{}
	mu    sync.Mutex
	state *os.ProcessState
}

func (t *acpTerminal) Write(p []byte) (int, error) { return t.out.Write(p) }

func (t *acpTerminal) kill() {
	if t.cmd.Process != nil {
		if runtime.GOOS == "windows" {
			_ = t.cmd.Process.Kill()
		} else {
			killProcessGroup(t.cmd)
		}
	}
}

func (t *acpTerminal) exitStatus() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state == nil {
		return nil
	}
	status := map[string]any{}
	if code := t.state.ExitCode(); code >= 0 {
		status["exitCode"] = code
	} else {
		status["signal"] = "terminated"
	}
	return status
}

func (c *acpClient) terminal(method string, raw json.RawMessage) (any, error) {
	switch method {
	case "terminal/create":
		return c.createTerminal(raw)
	case "terminal/output":
		return c.terminalOutput(raw)
	case "terminal/wait_for_exit":
		return c.waitTerminal(raw)
	case "terminal/kill":
		return c.killTerminal(raw)
	case "terminal/release":
		return c.releaseTerminal(raw)
	}
	return nil, errors.New(i18n.T("Method not supported"))
}

type terminalParams struct {
	TerminalID string `json:"terminalId"`
}

func (c *acpClient) findTerminal(raw json.RawMessage) (*acpTerminal, error) {
	var p terminalParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	c.termsMu.Lock()
	defer c.termsMu.Unlock()
	t := c.terms[p.TerminalID]
	if t == nil {
		return nil, errors.New(i18n.T("No such terminal"))
	}
	return t, nil
}

func (c *acpClient) createTerminal(raw json.RawMessage) (any, error) {
	var p struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Env     []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"env"`
		Cwd             string `json:"cwd"`
		OutputByteLimit int    `json:"outputByteLimit"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Command == "" {
		return nil, errors.New(i18n.T("No command"))
	}
	dir := c.root
	if p.Cwd != "" {
		if !within(c.root, p.Cwd) {
			return nil, errors.New(i18n.T("The agent asked to run a command outside the repository"))
		}
		dir = p.Cwd
	}
	limit := p.OutputByteLimit
	if limit <= 0 || limit > 4<<20 {
		limit = 1 << 20
	}
	cmd := exec.Command(p.Command, p.Args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for _, e := range p.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
	}
	t := &acpTerminal{cmd: cmd, out: &tailBuffer{limit: limit}, done: make(chan struct{})}
	cmd.Stdout = t
	cmd.Stderr = t
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		_ = cmd.Wait()
		t.mu.Lock()
		t.state = cmd.ProcessState
		t.mu.Unlock()
		close(t.done)
	}()
	id := "term_" + strconv.FormatInt(c.termSeq.Add(1), 10)
	c.termsMu.Lock()
	c.terms[id] = t
	c.termsMu.Unlock()
	return map[string]string{"terminalId": id}, nil
}

func (c *acpClient) terminalOutput(raw json.RawMessage) (any, error) {
	t, err := c.findTerminal(raw)
	if err != nil {
		return nil, err
	}
	output, truncated := t.out.snapshot()
	result := map[string]any{"output": output, "truncated": truncated}
	if status := t.exitStatus(); status != nil {
		result["exitStatus"] = status
	}
	return result, nil
}

func (c *acpClient) waitTerminal(raw json.RawMessage) (any, error) {
	t, err := c.findTerminal(raw)
	if err != nil {
		return nil, err
	}
	select {
	case <-t.done:
	case <-c.closed:
	case <-time.After(5 * time.Minute):
		return nil, errors.New(i18n.T("The command took too long"))
	}
	output, truncated := t.out.snapshot()
	result := map[string]any{"output": output, "truncated": truncated}
	if status := t.exitStatus(); status != nil {
		result["exitStatus"] = status
	}
	return result, nil
}

func (c *acpClient) killTerminal(raw json.RawMessage) (any, error) {
	t, err := c.findTerminal(raw)
	if err != nil {
		return nil, err
	}
	t.kill()
	return map[string]any{}, nil
}

func (c *acpClient) releaseTerminal(raw json.RawMessage) (any, error) {
	t, err := c.findTerminal(raw)
	if err != nil {
		return nil, err
	}
	t.kill()
	var p terminalParams
	_ = json.Unmarshal(raw, &p)
	c.termsMu.Lock()
	delete(c.terms, p.TerminalID)
	c.termsMu.Unlock()
	return map[string]any{}, nil
}

// snapshot is what the terminal has written, and whether its start was
// left out.
func (b *tailBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data), len(b.data) >= b.limit
}
