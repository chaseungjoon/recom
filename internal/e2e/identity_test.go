package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectIdentity(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "one", "same")
	b := filepath.Join(base, "two", "same")
	for _, d := range []string{a, b} {
		os.MkdirAll(d, 0o755)
		if r := run(t, d, "init"); r.code != 0 {
			t.Fatal(r)
		}
		mustRun(t, d, "true")
	}
	ra, rb := status(t, a).Replica, status(t, b).Replica
	if ra == rb {
		t.Fatal("two projects with the same folder name share a replica")
	}

	// cp -r (including .recom/) must not share the original's replica.
	cp := filepath.Join(base, "copy")
	if out, err := exec.Command("cp", "-a", a, cp).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	r := mustRun(t, cp, "true")
	if !strings.Contains(r.stderr, "is a copy of") {
		t.Fatalf("copy not detected: %s", r)
	}
	if rc := status(t, cp).Replica; rc == ra {
		t.Fatal("copied working copy shares the original replica")
	}
	if status(t, a).Replica != ra {
		t.Fatal("original identity changed")
	}

	// A git-clone-like copy (no .recom/) gets its own replica too.
	clone := filepath.Join(base, "clone")
	os.MkdirAll(clone, 0o755)
	os.WriteFile(filepath.Join(clone, ".recom.toml"), []byte(readFile(filepath.Join(a, ".recom.toml"))), 0o644)
	if status(t, clone).Replica == ra {
		t.Fatal("clone shares the original replica")
	}

	// Renaming the directory keeps the replica.
	moved := filepath.Join(base, "renamed")
	if err := os.Rename(b, moved); err != nil {
		t.Fatal(err)
	}
	r = mustRun(t, moved, "true")
	if !strings.Contains(r.stderr, "moved") || strings.Contains(r.stderr, "synced") {
		t.Fatalf("move should keep the replica without re-uploading: %s", r)
	}
	if status(t, moved).Replica != rb {
		t.Fatal("moved project changed replica")
	}
}

func TestUnusualProjectPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my proj ü $(x) ;'q'")
	os.MkdirAll(filepath.Join(dir, "sub dir"), 0o755)
	mustRun(t, dir, "init")
	write(t, dir, "sub dir/in.txt", "data")
	r := mustRun(t, filepath.Join(dir, "sub dir"), "--shell", "cat in.txt > out.txt")
	if b, _ := os.ReadFile(filepath.Join(dir, "sub dir", "out.txt")); string(b) != "data" {
		t.Fatalf("unusual project path: %s", r)
	}
}

func TestInitSafety(t *testing.T) {
	dir := newProject(t, "initsafe")
	r := run(t, dir, "init")
	if r.code == 0 || !strings.Contains(r.stderr, "already a recom project") {
		t.Fatalf("reinit: %s", r)
	}
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0o755)
	if r := run(t, sub, "init"); r.code == 0 || !strings.Contains(r.stderr, "inside the recom project") {
		t.Fatalf("nested init: %s", r)
	}
	home := t.TempDir()
	if r := runEnv(t, home, []string{"HOME=" + home}, "", "init"); r.code == 0 || !strings.Contains(r.stderr, "home directory") {
		t.Fatalf("init in $HOME: %s", r)
	}
}

func TestMalformedConfig(t *testing.T) {
	dir := newProject(t, "badcfg")
	write(t, dir, ".recom.toml", "id = \"not-a-uuid\"\n")
	if r := run(t, dir, "true"); r.code != 125 || !strings.Contains(r.stderr, "not a valid UUID") {
		t.Fatalf("bad project config: %s", r)
	}
	write(t, dir, ".recom.toml", "id = [[[\n")
	if r := run(t, dir, "true"); r.code != 125 || !strings.Contains(r.stderr, "not valid TOML") {
		t.Fatalf("broken TOML: %s", r)
	}
	cfgDir := t.TempDir()
	os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("default_host = 3\n"), 0o600)
	if r := runEnv(t, t.TempDir(), []string{"RECOM_CONFIG=" + filepath.Join(cfgDir, "config.toml")}, "", "--no-sync", "true"); r.code != 125 {
		t.Fatalf("broken global config: %s", r)
	}
}

func TestBackendOffline(t *testing.T) {
	dir := newProject(t, "offline")
	cfgPath := filepath.Join(env.dir, "config", "recom", "config.toml")
	orig := readFile(cfgPath)
	defer os.WriteFile(cfgPath, []byte(orig), 0o600)
	dead := "\n[hosts.deadhost]\nssh_target = \"deadhost\"\nremote_home = \"/tmp\"\nremote_root = \"/tmp/recom-dead\"\nos = \"linux\"\narch = \"amd64\"\nadded = 2026-01-01T00:00:00Z\n"
	os.WriteFile(cfgPath, []byte(orig+dead), 0o600)
	start := time.Now()
	r := run(t, dir, "--host", "deadhost", "true")
	if r.code != 255 || !strings.Contains(r.stderr, "cannot connect") {
		t.Fatalf("offline backend: %s", r)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("offline detection too slow")
	}
	if r := run(t, dir, "--host", "nope", "true"); r.code != 125 || !strings.Contains(r.stderr, "unknown backend") {
		t.Fatalf("unknown host: %s", r)
	}
}
