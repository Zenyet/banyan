// Banyan is a fast, native Git client in the spirit of JetBrains' Git
// tooling.
package main

import (
	"fmt"
	"os"

	"github.com/Zenyet/banyan/internal/app"
	"github.com/Zenyet/banyan/internal/askpass"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "0.2.0"

func main() {
	// Git runs this executable to ask for credentials; answer and exit.
	if socket := os.Getenv(askpass.SocketEnv); socket != "" {
		os.Exit(askpass.RunClient(socket))
	}
	requested := ""
	if len(os.Args) > 1 {
		requested = os.Args[1]
	}
	app.Version = version
	if err := app.Run(requested); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
