// Package agent implements the recom remote helper: a short-lived process
// started over SSH (never a daemon) that executes commands with exact argv,
// takes replica snapshots, attributes outputs to runs, supervises detached
// jobs and keeps durable manifests. It is built for Linux backends.
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	"github.com/chaseungjoon/recom/internal/buildinfo"
	"github.com/chaseungjoon/recom/internal/protocol"
)

// Main is the helper entry point; args exclude the program name and an
// optional leading "__agent".
func Main(args []string) int {
	if len(args) > 0 && args[0] == "__agent" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "recom-helper: this program is started by recom over SSH; it is not meant to be run by hand")
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(os.Stdin, os.Stdout)
	case "supervise":
		if len(args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: supervise <root> <replica> <job>")
			return 2
		}
		return supervise(args[1], args[2], args[3])
	case "dumpenv":
		return dumpEnv()
	case "version":
		sha, _ := selfSHA256()
		b, _ := json.Marshal(map[string]any{
			"version": buildinfo.Version, "commit": buildinfo.Commit,
			"protocol": protocol.Version, "sha256": sha, "os": runtime.GOOS, "arch": runtime.GOARCH,
		})
		fmt.Println(string(b))
		return 0
	default:
		fmt.Fprintf(os.Stderr, "recom-helper: unknown operation %q\n", args[0])
		return 2
	}
}

var (
	selfOnce sync.Once
	selfHash string
	selfErr  error
)

// selfSHA256 hashes the helper's own executable, letting the client verify
// that the installed helper is exactly the binary it uploaded.
func selfSHA256() (string, error) {
	selfOnce.Do(func() {
		p, err := os.Executable()
		if err != nil {
			selfErr = err
			return
		}
		f, err := os.Open(p)
		if err != nil {
			selfErr = err
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			selfErr = err
			return
		}
		selfHash = hex.EncodeToString(h.Sum(nil))
	})
	return selfHash, selfErr
}
