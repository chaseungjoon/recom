package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/pathmatch"
)

// parse resolves a command line the way cobra will, without running it.
func parse(t *testing.T, args ...string) (string, []string, *globalFlags) {
	t.Helper()
	root := NewRoot(&bytes.Buffer{}, &bytes.Buffer{})
	cmd, rest, err := root.Find(args)
	if err != nil {
		t.Fatalf("find %v: %v", args, err)
	}
	if err := cmd.ParseFlags(rest); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	gf := &globalFlags{}
	gf.noSync, _ = cmd.Flags().GetBool("no-sync")
	gf.host, _ = cmd.Flags().GetString("host")
	return cmd.Name(), cmd.Flags().Args(), gf
}

func TestRemoteCommandParsing(t *testing.T) {
	cases := []struct {
		args    []string
		cmd     string
		rest    []string
		noSync  bool
		hostSet string
	}{
		{[]string{"python3", "-m", "pytest", "-x", "--verbose"}, "recom", []string{"python3", "-m", "pytest", "-x", "--verbose"}, false, ""},
		{[]string{"--no-sync", "nvidia-smi"}, "recom", []string{"nvidia-smi"}, true, ""},
		{[]string{"-H", "gpu", "python3", "--help"}, "recom", []string{"python3", "--help"}, false, "gpu"},
		{[]string{"--", "env"}, "recom", []string{"env"}, false, ""},
		{[]string{"--", "ssh", "x"}, "recom", []string{"ssh", "x"}, false, ""},
		{[]string{"gcc", "status"}, "recom", []string{"gcc", "status"}, false, ""},
		{[]string{"status"}, "status", []string{}, false, ""},
		{[]string{"-v", "env"}, "env", []string{}, false, ""},
		{[]string{"logs", "abc", "-f"}, "logs", []string{"abc"}, false, ""},
	}
	for _, c := range cases {
		name, rest, gf := parse(t, c.args...)
		if name != c.cmd || strings.Join(rest, "\x00") != strings.Join(c.rest, "\x00") || gf.noSync != c.noSync || gf.host != c.hostSet {
			t.Errorf("%v → cmd=%s rest=%q nosync=%v host=%q", c.args, name, rest, gf.noSync, gf.host)
		}
	}
}

func TestUnknownFlagIsUsageError(t *testing.T) {
	root := NewRoot(&bytes.Buffer{}, &bytes.Buffer{})
	root.SetArgs([]string{"--bogus", "ls"})
	err := root.Execute()
	ee, ok := err.(*exitError)
	if !ok || ee.code != ExitUsage {
		t.Fatalf("got %v", err)
	}
}

func TestDisplayCommandRedacts(t *testing.T) {
	cases := map[string]string{
		"python3 train.py --lr 0.1":                 "python3 train.py --lr 0.1",
		"curl --token abc123 https://x":             "curl --token *** https://x",
		"tool --api-key=sk-123 run":                 "tool --api-key=*** run",
		"env AWS_SECRET_ACCESS_KEY=xyz OTHER=1 cmd": "env AWS_SECRET_ACCESS_KEY=*** OTHER=1 cmd",
		"mysql --password hunter2 db":               "mysql --password *** db",
	}
	for in, want := range cases {
		if got := displayCommand(strings.Fields(in)); got != want {
			t.Errorf("displayCommand(%q) = %q, want %q", in, got, want)
		}
	}
	if got := displayCommand([]string{"echo", "a b", "", "it's"}); got != `echo 'a b' '' 'it'\''s'` {
		t.Errorf("quoting: %q", got)
	}
}

func TestUploadRulesOrdering(t *testing.T) {
	p := &config.Project{}
	p.Sync.Include = []string{".env.ci"}
	p.Sync.Exclude = []string{"data/"}
	rules := uploadRules(p, []string{"ckpt/model*.pt"})
	idx := func(r string) int {
		for i, x := range rules {
			if x == r {
				return i
			}
		}
		t.Fatalf("rule %q missing from %v", r, rules)
		return -1
	}
	if !(idx("- /.recom/") < idx(`- /ckpt/model\*.pt`) && idx(`- /ckpt/model\*.pt`) < idx("+ .env.ci") && idx("+ .env.ci") < idx("- .env") && idx("- .env") < idx("- .git/") && idx("- .git/") < idx("- data/")) {
		t.Fatalf("rule order wrong: %v", rules)
	}
	f := false
	p.Sync.DefaultExcludes = &f
	for _, r := range uploadRules(p, nil) {
		if r == "- .git/" {
			t.Fatal("default excludes not disabled")
		}
		if r == "- .env" {
			goto ok
		}
	}
	t.Fatal("secret excludes must stay when default_excludes = false")
ok:
	for _, r := range rules {
		if _, err := pathmatch.Compile(strings.TrimPrefix(strings.TrimPrefix(r, "- "), "+ ")); err != nil {
			t.Errorf("rule %q does not compile: %v", r, err)
		}
	}
}

func TestLocalName(t *testing.T) {
	a := &app{}
	ws := &config.Workspace{Rel: "src/deep"}
	for in, want := range map[string]string{"src/deep/a.txt": "a.txt", "src/b.txt": "../b.txt", "out/c": "../../out/c"} {
		if got := a.localName(ws, in); got != want {
			t.Errorf("localName(%q) = %q, want %q", in, got, want)
		}
	}
}
