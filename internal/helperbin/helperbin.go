// Package helperbin provides the Linux helper binary that recom installs on
// backends. Release builds embed gzip-compressed helpers for linux/amd64 and
// linux/arm64 (built by `make helpers` / GoReleaser before the client), so a
// macOS client never deploys a macOS binary to Ubuntu.
package helperbin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
)

//go:embed assets
var assets embed.FS

// Helper is a helper binary for one platform.
type Helper struct {
	Data   []byte
	SHA256 string
	Source string // "embedded", "RECOM_HELPER_PATH", or "self"
	// SelfAgent is true when Data is the recom client itself, which serves
	// the helper protocol through its hidden "__agent" command.
	SelfAgent bool
}

// Name is the content-addressed file name used on the backend.
func (h *Helper) Name() string { return "recom-helper-" + h.SHA256[:16] }

var (
	mu    sync.Mutex
	cache = map[string]*Helper{}
)

// Embedded lists platforms with an embedded helper.
func Embedded() []string {
	var out []string
	for _, p := range []string{"linux/amd64", "linux/arm64"} {
		if _, err := assets.Open(assetName(p)); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func assetName(platform string) string {
	b := []byte(platform)
	for i, c := range b {
		if c == '/' {
			b[i] = '-'
		}
	}
	return "assets/recom-helper-" + string(b) + ".gz"
}

// For returns the helper for goos/goarch.
func For(goos, goarch string) (*Helper, error) {
	platform := goos + "/" + goarch
	mu.Lock()
	defer mu.Unlock()
	if h, ok := cache[platform]; ok {
		return h, nil
	}
	h, err := load(goos, goarch)
	if err != nil {
		return nil, err
	}
	cache[platform] = h
	return h, nil
}

func load(goos, goarch string) (*Helper, error) {
	platform := goos + "/" + goarch
	if goos != "linux" {
		return nil, fmt.Errorf("unsupported backend platform %s (recom backends must run Linux)", platform)
	}
	if p := os.Getenv("RECOM_HELPER_PATH"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("RECOM_HELPER_PATH: %w", err)
		}
		if err := checkELF(b, goarch); err != nil {
			return nil, fmt.Errorf("RECOM_HELPER_PATH %s: %w", p, err)
		}
		return newHelper(b, "RECOM_HELPER_PATH", false), nil
	}
	if f, err := assets.Open(assetName(platform)); err == nil {
		defer f.Close()
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("embedded helper for %s is corrupt: %w", platform, err)
		}
		b, err := io.ReadAll(zr)
		if err != nil {
			return nil, fmt.Errorf("embedded helper for %s is corrupt: %w", platform, err)
		}
		if err := checkELF(b, goarch); err != nil {
			return nil, fmt.Errorf("embedded helper for %s: %w", platform, err)
		}
		return newHelper(b, "embedded", false), nil
	}
	if runtime.GOOS == goos && runtime.GOARCH == goarch {
		self, err := os.Executable()
		if err == nil {
			if b, err := os.ReadFile(self); err == nil {
				return newHelper(b, "self", true), nil
			}
		}
	}
	return nil, fmt.Errorf("this recom build has no helper for %s backends; use a release build (`recom update`, or the install script) or build with `make build`", platform)
}

func newHelper(b []byte, src string, self bool) *Helper {
	sum := sha256.Sum256(b)
	return &Helper{Data: b, SHA256: hex.EncodeToString(sum[:]), Source: src, SelfAgent: self}
}

func checkELF(b []byte, goarch string) error {
	f, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("not a Linux ELF binary: %w", err)
	}
	want := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[goarch]
	if want == 0 || f.Machine != want {
		return fmt.Errorf("binary is for %v, backend needs %s", f.Machine, goarch)
	}
	return nil
}
