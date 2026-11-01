package agent

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// setupStampDir mirrors toolchain.StampDir (the helper does not import the
// client-side toolchain package).
const setupStampDir = ".recom/setup"

var stampNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// setupStamps reports which environment setups have completed in the
// replica. A stamp is "<fingerprint>\n<required path>\n", written by the
// setup script on success; it only counts while the required path (e.g.
// .venv/bin/python) still exists, so deleting an environment on the backend
// makes recom rebuild it.
func setupStamps(replica string) map[string]string {
	dir := filepath.Join(replica, filepath.FromSlash(setupStampDir))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range ents {
		if !e.Type().IsRegular() || !stampNameRe.MatchString(e.Name()) {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(f, 4096))
		f.Close()
		lines := strings.Split(string(b), "\n")
		fp := strings.TrimSpace(lines[0])
		if fp == "" {
			continue
		}
		if len(lines) > 1 {
			if req := strings.TrimSpace(lines[1]); req != "" {
				rel, err := CleanRel(req)
				if err != nil {
					continue
				}
				if _, err := os.Stat(filepath.Join(replica, filepath.FromSlash(rel))); err != nil {
					continue
				}
			}
		}
		out[e.Name()] = fp
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
