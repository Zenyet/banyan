// Package git is a thin wrapper over the git CLI, the same backend
// JetBrains IDEs use. A repository may be on this machine or on another
// one reached over SSH: every command and file access goes through the
// repository's transport.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Zenyet/banyan/internal/askpass"
	"github.com/Zenyet/banyan/internal/console"
)

// What git reads of the settings, which package settings sets.
var (
	// Program is the git executable to run.
	Program = func() string { return "git" }
	// IgnoreWhitespace leaves whitespace-only changes out of diffs.
	IgnoreWhitespace = func() bool { return false }
	// IsProtected reports whether a branch is never force pushed.
	IsProtected = func(branch string) bool { return false }
)

const (
	field  = "\x1f"
	record = "\x1e"
)

// GitVersion is the version of the git at program, e.g. "git version 2.50.1".
func GitVersion(program string) (string, error) {
	cmd := exec.Command(program, "--version")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cannot run %s: %w", program, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// command is git in dir on this machine.
func command(dir string, args []string) *exec.Cmd {
	return (&Repo{root: dir}).command(args)
}

// commandContext is git in dir on this machine, killed once ctx is done.
func commandContext(ctx context.Context, dir string, args []string) *exec.Cmd {
	return (&Repo{root: dir}).commandContext(ctx, args)
}

func (r *Repo) command(args []string) *exec.Cmd {
	return r.commandContextEnv(context.Background(), args, nil)
}

func (r *Repo) commandContext(ctx context.Context, args []string) *exec.Cmd {
	return r.commandContextEnv(ctx, args, nil)
}

// commandContextEnv is git in the repository, killed once ctx is done; env
// are extra KEY=VALUE settings, ahead of the repository's own environment.
func (r *Repo) commandContextEnv(ctx context.Context, args []string, env []string) *exec.Cmd {
	if r.ssh != nil {
		return r.ssh.commandContext(ctx, r.ssh.command(r.root, args, env))
	}
	full := append([]string{"-C", r.root, "-c", "color.ui=false", "-c", "core.quotepath=false"}, args...)
	cmd := exec.CommandContext(ctx, Program(), full...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		// Never touch the index behind the user's back (and don't wake our
		// own file watcher with it).
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
	)
	cmd.Env = append(cmd.Env, env...)
	// Credentials and ssh passphrases are asked for in the app.
	cmd.Env = append(cmd.Env, askpass.Env()...)
	hideWindow(cmd)
	return cmd
}

// run runs git in dir, recording the command in the console when record.
func run(dir string, args []string, recorded bool) (string, error) {
	return runInput(dir, args, nil, recorded)
}

func runInput(dir string, args []string, input []byte, recorded bool) (string, error) {
	r := &Repo{root: dir, recorded: recorded}
	return r.runContext(context.Background(), args, input)
}

func (r *Repo) run(args []string) (string, error) {
	return r.runContext(context.Background(), args, nil)
}

func (r *Repo) runInput(args []string, input []byte) (string, error) {
	return r.runContext(context.Background(), args, input)
}

func (r *Repo) runContext(ctx context.Context, args []string, input []byte) (string, error) {
	started := time.Now()
	cmd := r.commandContext(ctx, args)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	err := cmd.Run()
	elapsed := time.Since(started)
	if os.Getenv("BANYAN_TRACE") != "" {
		fmt.Fprintf(os.Stderr, "[%5d ms] git %s\n", elapsed.Milliseconds(), strings.Join(args, " "))
	}
	if r.recorded {
		console.Record(r.location(), args, stdout.String(), stderr.String(), err == nil, elapsed.Milliseconds())
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			if r.ssh != nil {
				return "", fmt.Errorf("failed to run ssh; is it installed? %w", err)
			}
			return "", fmt.Errorf("failed to run git; is it installed? (Settings › Git): %w", err)
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		return "", &Error{Args: args, Message: message}
	}
	return stdout.String(), nil
}

// runAllowDiff runs git diff --no-index, which exits with 1 when the files
// differ.
func (r *Repo) runAllowDiff(args []string) (string, error) {
	cmd := r.command(args)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err == nil || errors.As(err, &exit) && exit.ExitCode() == 1 {
		return stdout.String(), nil
	}
	return "", &Error{Args: args, Message: strings.TrimSpace(stderr.String())}
}

// ignoredPath is the null device as the repository's git sees it (a remote
// host is a Unix one).
func (r *Repo) ignoredPath() string {
	if r.ssh != nil {
		return "/dev/null"
	}
	return os.DevNull
}

// lines splits text into lines as Rust's str::lines does: no empty last
// line, and \r\n endings taken off.
func lines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	out := strings.Split(text, "\n")
	for i, l := range out {
		out[i] = strings.TrimSuffix(l, "\r")
	}
	return out
}

// nonEmpty splits text at sep, leaving out empty parts.
func nonEmpty(text, sep string) []string {
	var out []string
	for _, part := range strings.Split(text, sep) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
