package e2e

import (
	"os"
	"testing"
)

// TestDebugScript runs RECOM_E2E_DEBUG commands (one per line, args split on
// "|") in a fresh project and prints their output. For manual exploration.
func TestDebugScript(t *testing.T) {
	f := os.Getenv("RECOM_E2E_DEBUG")
	if f == "" {
		t.Skip("set RECOM_E2E_DEBUG to a script file")
	}
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	dir := newProject(t, "dbg")
	write(t, dir, "src/run.py", "import os\nos.makedirs('outputs', exist_ok=True)\nopen('outputs/r.txt','w').write('x')\nprint('cwd', os.getcwd())\n")
	for _, line := range splitLines(script) {
		args := splitArgs(line)
		r := run(t, dir, args...)
		t.Logf("$ recom %v\n%s", args, r)
	}
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '\n' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func splitArgs(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '|' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	return append(out, cur)
}
