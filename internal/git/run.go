// Package git is a thin wrapper over the git CLI, the same backend
// JetBrains IDEs use.
package git

import (
	"bytes"
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
	out, err := exec.Command(program, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("cannot run %s: %w", program, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func command(dir string, args []string) *exec.Cmd {
	full := append([]string{"-C", dir, "-c", "color.ui=false", "-c", "core.quotepath=false"}, args...)
	cmd := exec.Command(Program(), full...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		// Never touch the index behind the user's back (and don't wake our
		// own file watcher with it).
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
	)
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
	started := time.Now()
	cmd := command(dir, args)
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
	if recorded {
		console.Record(dir, args, stdout.String(), stderr.String(), err == nil, elapsed.Milliseconds())
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
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
func runAllowDiff(dir string, args []string) (string, error) {
	cmd := command(dir, args)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err == nil || errors.As(err, &exit) && exit.ExitCode() == 1 {
		return stdout.String(), nil
	}
	return "", &Error{Args: args, Message: strings.TrimSpace(stderr.String())}
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
