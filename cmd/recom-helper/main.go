// Command recom-helper is the remote side of recom. The recom client uploads
// it to the backend's private application directory (content-addressed by
// SHA-256) and starts it over SSH for each session. It is not a daemon.
package main

import (
	"os"

	"github.com/chaseungjoon/recom/internal/agent"
)

func main() {
	os.Exit(agent.Main(os.Args[1:]))
}
