package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"runtime"
	"sort"
	"strings"

	"github.com/Zenyet/banyan/internal/askpass"
)

// SSH is where a repository is reached: a destination ssh(1) accepts, such
// as user@host, with the user's own configuration (keys, agent, ProxyJump,
// ControlMaster) from ~/.ssh/config.
type SSH struct {
	Host string
}

// SSHProgram is the ssh executable: BANYAN_SSH when set, else ssh from the
// PATH.
func SSHProgram() string {
	if p := os.Getenv("BANYAN_SSH"); p != "" {
		return p
	}
	return "ssh"
}

// QuoteArg quotes s for a POSIX shell, the shell ssh(1) runs commands with.
func QuoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\r'\"\\$`;&|<>()*?[]{}!#~=%") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// controlPath is one socket per app run for ssh to share one connection
// between commands: without it a password or a key's passphrase is asked
// for on every git command. It is short enough for a Unix socket's path
// limit; Windows opens connections of its own.
var controlPath = func() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	// A random, private folder: another user's socket of a guessed name must
	// never be used.
	dir, err := os.MkdirTemp("/tmp", "banyan-ssh-")
	if err != nil {
		return ""
	}
	return dir + "/%C"
}

// commandContext is ssh running script on the host, killed once ctx is
// done.
func (s *SSH) commandContext(ctx context.Context, script string) *exec.Cmd {
	// A host that is down otherwise hangs for the system's TCP timeout.
	args := []string{"-o", "ConnectTimeout=20"}
	if path := controlPath(); path != "" {
		args = append(args, "-o", "ControlMaster=auto", "-o", "ControlPath="+path, "-o", "ControlPersist=300")
	}
	// The login shell of the host parses the command, and may not be a
	// POSIX one: sh runs it, whatever the user's shell is.
	args = append(args, s.Host, "sh -c "+QuoteArg(script))
	cmd := exec.CommandContext(ctx, SSHProgram(), args...)
	// Passphrases, passwords and the host key's confirmation are asked for
	// in the app, as git's own ssh is.
	cmd.Env = append(os.Environ(), askpass.Env()...)
	hideWindow(cmd)
	return cmd
}

// command is the script git runs in root, with the settings Banyan gives
// every git command, and env on top.
func (s *SSH) command(root string, args []string, env []string) string {
	var b strings.Builder
	b.WriteString("env GIT_TERMINAL_PROMPT=0 GIT_OPTIONAL_LOCKS=0 LC_ALL=C")
	for _, kv := range env {
		b.WriteByte(' ')
		b.WriteString(kv)
	}
	b.WriteString(" git -C ")
	b.WriteString(QuoteArg(root))
	b.WriteString(" -c color.ui=false -c core.quotepath=false")
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(QuoteArg(a))
	}
	return b.String()
}

// runScript runs script on the host and returns what it writes to stdout.
func (s *SSH) runScript(ctx context.Context, script string, input []byte) ([]byte, error) {
	cmd := s.commandContext(ctx, script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, &sshError{host: s.Host, message: message, missing: missingFile(message)}
	}
	return stdout.Bytes(), nil
}

// sshError is what ssh(1), or the command it ran, reported.
type sshError struct {
	host    string
	message string
	missing bool
}

func (e *sshError) Error() string { return e.host + ": " + e.message }
func (e *sshError) Unwrap() error {
	if e.missing {
		return os.ErrNotExist
	}
	return nil
}

// missingFile reports whether ssh's message says a path does not exist.
func missingFile(message string) bool {
	return strings.Contains(message, "No such file") || strings.Contains(message, "no such file")
}

// expand resolves a path starting with ~ to the host's home folder, since
// git is given the path quoted and would not expand it itself.
func (s *SSH) expand(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	rest := strings.TrimPrefix(path, "~")
	out, err := s.runScript(context.Background(), `cd "$HOME"`+QuoteArg(rest)+" && pwd -P", nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ConnectSSH checks that host answers over ssh, and returns the connection
// to choose a repository on it.
func ConnectSSH(host string) (*SSH, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("a repository over SSH needs a host")
	}
	ssh := &SSH{Host: host}
	if _, err := ssh.Home(); err != nil {
		return nil, err
	}
	return ssh, nil
}

// Home is the host's home folder, with symlinks resolved.
func (s *SSH) Home() (string, error) {
	out, err := s.runScript(context.Background(), `cd "$HOME" && pwd -P`, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Dirs lists the folders in dir, a path on the host: it returns the
// resolved path and the folders' names, sorted, leaving out .git.
func (s *SSH) Dirs(dir string) (string, []string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "~"
	}
	resolved, err := s.expand(dir)
	if err != nil {
		return "", nil, err
	}
	script := "cd " + QuoteArg(resolved) + " && pwd -P && find . -mindepth 1 -maxdepth 1 -type d -print0"
	out, err := s.runScript(context.Background(), script, nil)
	if err != nil {
		return "", nil, err
	}
	at := strings.IndexByte(string(out), '\n')
	if at < 0 {
		return "", nil, fmt.Errorf("%s: cannot list the folders", dir)
	}
	resolved = strings.TrimSpace(string(out[:at]))
	var names []string
	for _, entry := range strings.Split(string(out[at+1:]), "\x00") {
		if entry == "" {
			continue
		}
		name := path.Base(entry)
		if name == ".git" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return resolved, names, nil
}

// Open opens the repository at dir on the host.
func (s *SSH) Open(dir string) (*Repo, error) { return OpenSSH(s.Host, dir) }

// SSHLocation is where a repository on host at path is remembered and
// named, as ssh://user@host/path.
func SSHLocation(host, path string) string {
	return "ssh://" + host + "/" + strings.TrimLeft(path, "/")
}

// ParseLocation splits a location as SSHLocation writes it; ok is false for
// a path on this machine.
func ParseLocation(location string) (host, path string, ok bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(location), "ssh://")
	if !ok {
		return "", "", false
	}
	at := strings.IndexByte(rest, '/')
	if at <= 0 {
		return "", "", false
	}
	return rest[:at], rest[at:], true
}

// OpenLocation opens a repository named by a location: ssh://user@host/path
// for one over SSH, else a folder on this machine.
func OpenLocation(location string) (*Repo, error) {
	if host, path, ok := ParseLocation(location); ok {
		return OpenSSH(host, path)
	}
	return Open(location)
}

// OpenSSH opens the repository at path on host, running its git through
// ssh(1) and reading its files the same way. The path may start with ~ for
// the host's home folder.
func OpenSSH(host, path string) (*Repo, error) {
	host = strings.TrimSpace(host)
	path = strings.TrimSpace(path)
	if host == "" || path == "" {
		return nil, fmt.Errorf("a repository over SSH needs a host and a path")
	}
	ssh := &SSH{Host: host}
	resolved, err := ssh.expand(path)
	if err != nil {
		return nil, err
	}
	return open(&Repo{root: resolved, ssh: ssh})
}

// SSHCommand is ssh running script on host, whose standard streams connect
// to the caller's: an ACP agent there speaks over them.
func SSHCommand(ctx context.Context, host, script string) *exec.Cmd {
	return (&SSH{Host: host}).commandContext(ctx, script)
}
