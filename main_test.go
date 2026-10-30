//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Git asks for credentials through the banyan executable in askpass mode.
func TestGitGetsCredentialsFromTheApp(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "banyan")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	// Unix socket paths are short; a test's own temporary folder is too
	// deep on macOS.
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("banyan-askpass-it-%d.sock", os.Getpid()))
	if len(socket) > 100 {
		socket = fmt.Sprintf("/tmp/banyan-askpass-it-%d.sock", os.Getpid())
	}
	os.Remove(socket)
	defer os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// Stand in for the app: answer the username and password prompts.
	prompts := make(chan string, 2)
	go func() {
		for range 2 {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			prompt, _ := bufio.NewReader(conn).ReadString('\n')
			answer := "s3cret"
			if strings.HasPrefix(prompt, "Username") {
				answer = "alice"
			}
			fmt.Fprintf(conn, "OK %s\n", answer)
			conn.Close()
			prompts <- strings.TrimSpace(prompt)
		}
	}()

	git := exec.Command("git", "-c", "credential.helper=", "credential", "fill")
	git.Env = append(os.Environ(), "GIT_ASKPASS="+exe, "BANYAN_ASKPASS_SOCKET="+socket, "GIT_TERMINAL_PROMPT=0")
	git.Stdin = strings.NewReader("protocol=https\nhost=example.com\n\n")
	out, err := git.Output()
	if err != nil {
		t.Fatalf("git failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "username=alice") || !strings.Contains(string(out), "password=s3cret") {
		t.Fatalf("output:\n%s", out)
	}
	if p := <-prompts; !strings.HasPrefix(p, "Username for 'https://example.com'") {
		t.Errorf("first prompt %q", p)
	}
	if p := <-prompts; !strings.HasPrefix(p, "Password for 'https://alice@example.com'") {
		t.Errorf("second prompt %q", p)
	}
}
