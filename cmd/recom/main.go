// Command recom runs commands on a remote Linux machine over SSH with your
// project synced automatically: write locally, compute remotely, bring
// generated files home.
package main

import (
	"os"

	"github.com/chaseungjoon/recom/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
