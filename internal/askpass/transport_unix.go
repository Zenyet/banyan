//go:build !windows

package askpass

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// serve listens on a socket file only this user can open.
func serve(onConnect func(net.Conn)) (string, error) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("banyan-askpass-%d.sock", os.Getpid()))
	os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
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
	return path, nil
}

func connect(address string) (net.Conn, error) {
	return net.Dial("unix", address)
}

// accepts any client: the file's permissions already keep other users out.
func accepts(*bufio.Reader) bool { return true }
