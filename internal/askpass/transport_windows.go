//go:build windows

package askpass

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync"
)

// A loopback port; clients prove they were started by this app with a
// random token, passed as port:token.

var token = sync.OnceValue(func() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
})

func serve(onConnect func(net.Conn)) (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			onConnect(conn)
		}
	}()
	return fmt.Sprintf("%d:%s", listener.Addr().(*net.TCPAddr).Port, token()), nil
}

func connect(address string) (net.Conn, error) {
	port, tok, ok := strings.Cut(address, ":")
	if !ok {
		return nil, fmt.Errorf("bad address")
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintln(conn, tok); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func accepts(reader *bufio.Reader) bool {
	line, err := reader.ReadString('\n')
	return err == nil && strings.TrimRight(line, "\r\n") == token()
}
