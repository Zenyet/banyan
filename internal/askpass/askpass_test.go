package askpass

import (
	"net"
	"testing"
)

func TestPromptRoundTrip(t *testing.T) {
	requests := make(chan *Request)
	addr, err := serve(func(conn net.Conn) { go handle(conn, requests) })
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		first := <-requests
		if first.Prompt != "Username for 'https://example.com': " {
			t.Errorf("prompt = %q", first.Prompt)
		}
		alice := "alice"
		first.Reply(&alice)
		second := <-requests
		second.Reply(nil)
	}()
	if answer, ok := ask(addr, "Username for 'https://example.com': "); !ok || answer != "alice" {
		t.Errorf("answer = %q, %v", answer, ok)
	}
	if _, ok := ask(addr, "Password for 'https://alice@example.com': "); ok {
		t.Error("a cancelled prompt has no answer")
	}
}

func TestSecrets(t *testing.T) {
	if !isSecret("Password for 'https://example.com': ") || !isSecret("Enter passphrase for key '/Users/me/.ssh/id_ed25519': ") {
		t.Error("passwords and passphrases are secret")
	}
	if isSecret("Username for 'https://example.com': ") {
		t.Error("user names are not")
	}
}
