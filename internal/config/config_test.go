package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := map[string]int64{"100MB": 100 << 20, "1.5GiB": 3 << 29, "0": 0, "500k": 500 << 10, "2 GB": 2 << 30, "7": 7, "1TB": 1 << 40}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "-1MB", "ten", "1XB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
}

func TestUUID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		u := NewUUID()
		if !uuidRe.MatchString(u) || seen[u] {
			t.Fatalf("bad uuid %q", u)
		}
		seen[u] = true
	}
}

func TestInitDiscoverAndTemplate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	p, err := Init(root, "gpu", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Host != "gpu" {
		t.Fatalf("host not recorded: %+v", p)
	}
	if _, err := Init(root, "", nil, nil); err == nil || !strings.Contains(err.Error(), "already a recom project") {
		t.Fatalf("reinit: %v", err)
	}
	if _, err := Init(filepath.Join(root, "a"), "", nil, nil); err == nil {
		t.Fatal("nested init accepted")
	}
	ws, err := Discover(filepath.Join(root, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if ws.Rel != "a/b" || ws.Project.ID != p.ID || ws.Local == nil || len(ws.ReplicaID()) < 40 {
		t.Fatalf("workspace %+v", ws)
	}
	// The template's defaults parse to the documented values.
	if n, _ := ws.Project.MaxFileSize(); n != 100<<20 {
		t.Fatalf("max_file_size %d", n)
	}
	if !ws.Project.Sync.UseDefaultExcludes() {
		t.Fatal("default excludes off")
	}
	gi, _ := os.ReadFile(filepath.Join(root, ".recom", ".gitignore"))
	if !strings.Contains(string(gi), "*") {
		t.Fatal(".recom/ does not ignore itself")
	}
	if _, err := Discover(t.TempDir()); err != ErrNoProject {
		t.Fatalf("outside project: %v", err)
	}
}

func TestInitRefusesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := Init(home, "", nil, nil); err == nil {
		t.Fatal("init in $HOME accepted")
	}
}

func TestWorkingCopyIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	orig := filepath.Join(base, "orig")
	os.MkdirAll(orig, 0o755)
	if _, err := Init(orig, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	w1, _ := Discover(orig)
	id := w1.ReplicaID()
	w1b, _ := Discover(orig)
	if w1b.ReplicaID() != id {
		t.Fatal("identity not stable")
	}
	// Copy including .recom/: new identity, original keeps its own.
	cp := filepath.Join(base, "copy")
	copyTree(t, orig, cp)
	w2, _ := Discover(cp)
	if w2.ReplicaID() == id || !strings.Contains(w2.Notice, "copy") {
		t.Fatalf("copy shares identity: %s %q", w2.ReplicaID(), w2.Notice)
	}
	// Move: identity kept.
	mv := filepath.Join(base, "moved")
	os.Rename(orig, mv)
	w3, _ := Discover(mv)
	if w3.ReplicaID() != id || !strings.Contains(w3.Notice, "moved") {
		t.Fatalf("move changed identity: %s %q", w3.ReplicaID(), w3.Notice)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, _ := os.ReadFile(p)
		return os.WriteFile(target, b, fi.Mode())
	})
}

func TestProjectValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) error {
		p := filepath.Join(dir, ProjectFile)
		os.WriteFile(p, []byte(s), 0o644)
		_, err := LoadProject(p)
		return err
	}
	good := `id = "` + NewUUID() + `"` + "\n"
	if err := write(good); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"bad id":      `id = "x"`,
		"unknown key": good + "colour = 1\n",
		"bad size":    good + "[outputs]\nmax_file_size = \"lots\"\n",
		"bad pattern": good + "[sync]\nexclude = [\"[abc\"]\n",
		"bad mode":    good + "[run]\nenv_mode = \"bash\"\n",
		"bad tmux":    good + "[jobs]\ntmux = \"sometimes\"\n",
		"bad env":     good + "[run]\nenv = { \"A=B\" = \"1\" }\n",
	} {
		if err := write(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestGlobalConfigRoundTrip(t *testing.T) {
	t.Setenv("RECOM_CONFIG", filepath.Join(t.TempDir(), "c", "config.toml"))
	g, err := LoadGlobal()
	if err != nil || len(g.Hosts) != 0 {
		t.Fatalf("empty: %v %+v", err, g)
	}
	g.DefaultHost = "gpu"
	g.Hosts["gpu"] = &Host{Target: "me@gpu", Port: 2222, RemoteHome: "/home/me", RemoteRoot: "/home/me/.local/share/recom", OS: "linux", Arch: "amd64"}
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(GlobalPath())
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", fi.Mode())
	}
	g2, err := LoadGlobal()
	if err != nil || g2.DefaultHost != "gpu" || g2.Hosts["gpu"].Port != 2222 || !g2.Hosts["gpu"].UseMultiplex() {
		t.Fatalf("round trip: %v %+v", err, g2)
	}
	os.WriteFile(GlobalPath(), []byte("[hosts.x]\nport = 1\n"), 0o600)
	if _, err := LoadGlobal(); err == nil {
		t.Fatal("incomplete host accepted")
	}
}
