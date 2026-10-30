// Package askpass asks for credentials for git and ssh, like JetBrains'
// "Git Authentication" dialog.
//
// Git runs this same executable as GIT_ASKPASS / SSH_ASKPASS. In that mode it
// forwards the prompt to the running app, over a Unix socket or on Windows a
// loopback port, and the app asks the user and sends the answer back.
package askpass

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// SocketEnv is set in git's environment; its presence puts this executable
// in client mode.
const SocketEnv = "BANYAN_ASKPASS_SOCKET"

// answerTimeout is how long git may wait for the user before the prompt
// counts as cancelled.
const answerTimeout = 10 * time.Minute

// Request is a prompt from git; Reply answers it once, nil for cancel.
type Request struct {
	Prompt string
	reply  chan *string
	once   sync.Once
}

// Reply answers the prompt, with nil to cancel it. Only the first reply
// counts.
func (r *Request) Reply(answer *string) {
	r.once.Do(func() { r.reply <- answer })
}

// IsSecret reports whether the prompt asks for a password or the like,
// which the dialog masks.
func (r *Request) IsSecret() bool { return isSecret(r.Prompt) }

var address string

// Env is the environment for git commands once the server runs.
func Env() []string {
	if address == "" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}
	return []string{
		"GIT_ASKPASS=" + exe,
		"SSH_ASKPASS=" + exe,
		"SSH_ASKPASS_REQUIRE=force",
		SocketEnv + "=" + address,
		"DISPLAY=" + display,
	}
}

// RunClient is client mode: forward the prompt and print the answer for
// git. It returns the exit code.
func RunClient(socket string) int {
	prompt := ""
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}
	answer, ok := ask(socket, prompt)
	if !ok {
		return 1
	}
	fmt.Println(answer)
	return 0
}

func ask(address, prompt string) (string, bool) {
	conn, err := connect(address)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, strings.ReplaceAll(prompt, "\n", " ")); err != nil {
		return "", false
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", false
	}
	return strings.CutPrefix(strings.TrimSuffix(line, "\n"), "OK ")
}

// StartServer listens for prompts; each arrives on the returned channel.
func StartServer() (<-chan *Request, error) {
	requests := make(chan *Request)
	addr, err := serve(func(conn net.Conn) { go handle(conn, requests) })
	if err != nil {
		return nil, err
	}
	address = addr
	return requests, nil
}

func handle(conn net.Conn, requests chan<- *Request) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if !accepts(reader) {
		return
	}
	prompt, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	request := &Request{Prompt: strings.TrimRight(prompt, "\r\n"), reply: make(chan *string, 1)}
	select {
	case requests <- request:
	case <-time.After(answerTimeout):
		return
	}
	select {
	case answer := <-request.reply:
		if answer != nil {
			fmt.Fprintf(conn, "OK %s\n", *answer)
			return
		}
	case <-time.After(answerTimeout):
	}
	fmt.Fprintln(conn, "CANCEL")
}

func isSecret(prompt string) bool {
	prompt = strings.ToLower(prompt)
	return strings.Contains(prompt, "password") || strings.Contains(prompt, "passphrase") || strings.Contains(prompt, "token")
}
