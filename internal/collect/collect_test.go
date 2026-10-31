package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/chaseungjoon/recom/internal/protocol"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestSafeRel(t *testing.T) {
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", "a/./b", "a//b", ".recom/local.toml", ".recom", ".recom.toml", "a\x00b", "."} {
		if _, err := SafeRel(bad); err == nil {
			t.Errorf("SafeRel(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"a", "a/b.txt", "-dash", "x/.recom/y", "sp ace/ü"} {
		if _, err := SafeRel(ok); err != nil {
			t.Errorf("SafeRel(%q): %v", ok, err)
		}
	}
}

type fixture struct {
	t    *testing.T
	root string
	c    *Collector
	r    *os.Root
}

func newFixture(t *testing.T) *fixture {
	root := t.TempDir()
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return &fixture{t: t, root: root, c: &Collector{Root: root}, r: r}
}

func (f *fixture) local(rel, content string) {
	p := filepath.Join(f.root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) stage(rel, content string) {
	f.local(filepath.Join(".recom/staging/b1", rel), content)
}

func (f *fixture) install(o protocol.Output) Result {
	res := Result{Output: o}
	f.c.installOne(f.r, filepath.Join(".recom", "staging", "b1"), "b1", o, &res)
	return res
}

func (f *fixture) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(f.root, rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestInstallRules(t *testing.T) {
	t.Run("new file", func(t *testing.T) {
		f := newFixture(t)
		f.stage("out/a.txt", "remote")
		r := f.install(protocol.Output{Path: "out/a.txt", Type: "file", SHA256: sum("remote")})
		if r.Status != Collected || f.read("out/a.txt") != "remote" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("overwrite unchanged local copy", func(t *testing.T) {
		f := newFixture(t)
		f.local("log.txt", "v1")
		f.stage("log.txt", "v2")
		r := f.install(protocol.Output{Path: "log.txt", Type: "file", SHA256: sum("v2"), PreExisted: true, PreSHA256: sum("v1")})
		if r.Status != Collected || f.read("log.txt") != "v2" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("local edit is a conflict", func(t *testing.T) {
		f := newFixture(t)
		f.local("log.txt", "edited locally")
		f.stage("log.txt", "v2")
		r := f.install(protocol.Output{Path: "log.txt", Type: "file", SHA256: sum("v2"), PreExisted: true, PreSHA256: sum("v1")})
		if r.Status != Conflict || f.read("log.txt") != "edited locally" || f.read(r.Staged) != "v2" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("local file appeared for a new output", func(t *testing.T) {
		f := newFixture(t)
		f.local("new.txt", "mine")
		f.stage("new.txt", "theirs")
		r := f.install(protocol.Output{Path: "new.txt", Type: "file", SHA256: sum("theirs")})
		if r.Status != Conflict || f.read("new.txt") != "mine" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("identical content", func(t *testing.T) {
		f := newFixture(t)
		f.local("same.txt", "x")
		f.stage("same.txt", "x")
		r := f.install(protocol.Output{Path: "same.txt", Type: "file", SHA256: sum("x")})
		if r.Status != Collected || !r.Identity {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("checksum mismatch stays pending", func(t *testing.T) {
		f := newFixture(t)
		f.stage("c.txt", "truncat")
		r := f.install(protocol.Output{Path: "c.txt", Type: "file", SHA256: sum("truncated")})
		if r.Status != Failed || f.read("c.txt") != "<missing>" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("not downloaded", func(t *testing.T) {
		f := newFixture(t)
		r := f.install(protocol.Output{Path: "x.txt", Type: "file", SHA256: sum("x")})
		if r.Status != Failed {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("force keeps a backup", func(t *testing.T) {
		f := newFixture(t)
		f.c.Force = true
		f.local("f.txt", "mine")
		f.stage("f.txt", "theirs")
		r := f.install(protocol.Output{Path: "f.txt", Type: "file", SHA256: sum("theirs")})
		if r.Status != Collected || f.read("f.txt") != "theirs" || f.read(r.Backup) != "mine" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("directory in the way", func(t *testing.T) {
		f := newFixture(t)
		os.MkdirAll(filepath.Join(f.root, "d"), 0o755)
		f.stage("d", "file")
		r := f.install(protocol.Output{Path: "d", Type: "file", SHA256: sum("file")})
		if r.Status != Conflict {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("symlinked parent escaping the project", func(t *testing.T) {
		f := newFixture(t)
		outside := t.TempDir()
		os.Symlink(outside, filepath.Join(f.root, "escape"))
		f.stage("escape/pwned.txt", "x")
		r := f.install(protocol.Output{Path: "escape/pwned.txt", Type: "file", SHA256: sum("x")})
		if r.Status == Collected {
			t.Fatalf("installed through an escaping symlink: %+v", r)
		}
		if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); err == nil {
			t.Fatal("file written outside the project")
		}
	})
	t.Run("symlink output", func(t *testing.T) {
		f := newFixture(t)
		r := f.install(protocol.Output{Path: "link", Type: "symlink", Link: "target.txt"})
		if l, _ := os.Readlink(filepath.Join(f.root, "link")); r.Status != Collected || l != "target.txt" {
			t.Fatalf("%+v %q", r, l)
		}
	})
}
