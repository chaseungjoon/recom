package agent

import (
	"bufio"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/chaseungjoon/recom/internal/pathmatch"
	"github.com/chaseungjoon/recom/internal/policy"
	"github.com/chaseungjoon/recom/internal/protocol"
)

var probeTools = []struct {
	name string
	args []string
}{
	{"python3", []string{"--version"}},
	{"python", []string{"--version"}},
	{"pip3", []string{"--version"}},
	{"uv", []string{"--version"}},
	{"conda", []string{"--version"}},
	{"node", []string{"--version"}},
	{"cargo", []string{"--version"}},
	{"go", []string{"version"}},
	{"gcc", []string{"--version"}},
	{"g++", []string{"--version"}},
	{"clang", []string{"--version"}},
	{"make", []string{"--version"}},
	{"cmake", []string{"--version"}},
	{"nvcc", []string{"--version"}},
	{"nvidia-smi", nil},
	{"git", []string{"--version"}},
	{"rsync", []string{"--version"}},
	{"tmux", []string{"-V"}},
}

func osPretty() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return runtime.GOOS
}

func (s *server) envReport(r *protocol.EnvRequest) (*protocol.Response, error) {
	replica := ""
	if r.ReplicaID != "" {
		if err := s.selectReplica(r.ReplicaID); err != nil {
			return nil, err
		}
		if s.replicaExists() {
			replica = s.replicaPath
		}
	}
	env, warn := buildEnv(r.EnvMode, r.Env, r.Path, r.PathFallback, nil, replica)
	envList := env.list()
	dir := s.home
	if replica != "" {
		dir = replica
	}
	res := &protocol.EnvResult{OS: osPretty(), Arch: runtime.GOARCH, Shell: userShell(), EnvMode: r.EnvMode, PATH: env["PATH"], Root: s.layout.Root, EnvWarning: warn}
	if res.EnvMode == "" {
		res.EnvMode = EnvLogin
	}
	res.Hostname, _ = os.Hostname()
	res.Kernel = kernelRelease()
	ctx := context.Background()
	for _, t := range probeTools {
		p, err := lookPath(t.name, env["PATH"], dir)
		if err != nil {
			res.Tools = append(res.Tools, protocol.Tool{Name: t.name})
			continue
		}
		tool := protocol.Tool{Name: t.name, Path: p}
		if t.args != nil {
			tool.Version = firstLine(ctx, envList, p, t.args...)
			if t.name == "nvcc" {
				tool.Version = lastMatching(ctx, envList, p, "release")
			}
		}
		switch t.name {
		case "nvidia-smi":
			out := firstLines(ctx, envList, p, "--query-gpu=name,driver_version,memory.total", "--format=csv,noheader")
			if len(out) == 0 {
				res.GPUNote = "nvidia-smi is installed but reported no GPU (driver not loaded?)"
			}
			res.GPUs = out
		case "tmux":
			res.TmuxOK = true
		case "rsync":
			res.RsyncOK = true
		}
		res.Tools = append(res.Tools, tool)
	}
	if len(res.GPUs) == 0 && res.GPUNote == "" {
		res.GPUNote = "no NVIDIA GPU tooling found (nvidia-smi not on PATH)"
	}
	var st syscall.Statfs_t
	if syscall.Statfs(s.layout.Root, &st) == nil {
		res.DiskFree = st.Bavail * uint64(st.Bsize)
		res.DiskTotal = st.Blocks * uint64(st.Bsize)
	}
	if f, err := os.CreateTemp(s.layout.Root, ".write-test-*"); err == nil {
		f.Close()
		os.Remove(f.Name())
		res.Writable = true
	}
	return &protocol.Response{Env: res}, nil
}

func firstLines(ctx context.Context, env []string, path string, args ...string) []string {
	var out []string
	for _, line := range strings.Split(runOutput(ctx, env, path, args...), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.Contains(line, "NVIDIA-SMI has failed") {
			out = append(out, line)
		}
	}
	return out
}

func lastMatching(ctx context.Context, env []string, path, needle string) string {
	for _, line := range strings.Split(runOutput(ctx, env, path, "--version"), "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// stat resolves explicit pull paths into concrete files.
func (s *server) stat(r *protocol.StatRequest) (*protocol.Response, error) {
	if err := s.selectReplica(r.ReplicaID); err != nil {
		return nil, err
	}
	if !s.replicaExists() {
		return nil, perr(protocol.ErrNoReplica, "the project has no replica on this backend yet")
	}
	pending, _ := pendingOutputs(s.layout, s.replica)
	byPath := map[string]protocol.Output{}
	for _, o := range pending {
		byPath[o.Path] = o
	}
	secrets := pathmatch.MustCompileSet(policy.Secrets)
	allow := pathmatch.MustCompileSet(policy.SecretAllow)
	excl := pathmatch.MustCompileSet(policy.OutputExcludes)
	res := &protocol.StatResult{}
	seen := map[string]bool{}
	add := func(rel, abs string, fi fs.FileInfo) {
		if seen[rel] {
			return
		}
		seen[rel] = true
		o := protocol.Output{Path: rel, Size: fi.Size(), Mode: uint32(fi.Mode().Perm()), MtimeNs: fi.ModTime().UnixNano(), Status: protocol.StatusPending}
		if fi.Mode()&fs.ModeSymlink != 0 {
			o.Type = "symlink"
			o.Link, _ = os.Readlink(abs)
			if !safeLink(rel, o.Link) {
				res.Warnings = append(res.Warnings, rel+": symlink points outside the project; skipped")
				return
			}
		} else {
			o.Type = "file"
			h, err := hashFile(abs)
			if err != nil {
				res.Warnings = append(res.Warnings, rel+": "+err.Error())
				return
			}
			o.SHA256 = h
		}
		if p, ok := byPath[rel]; ok {
			o.RunID, o.PreExisted, o.PreSHA256 = p.RunID, p.PreExisted, p.PreSHA256
		}
		res.Files = append(res.Files, o)
	}
	for _, raw := range r.Paths {
		rel, err := CleanRel(raw)
		if err != nil {
			return nil, perr(protocol.ErrBadRequest, "%v", err)
		}
		abs, err := insideNoSymlink(s.replicaPath, rel, true)
		if err != nil {
			res.Missing = append(res.Missing, raw)
			continue
		}
		fi, err := os.Lstat(abs)
		if err != nil {
			res.Missing = append(res.Missing, raw)
			continue
		}
		if !fi.IsDir() {
			if fi.Mode().IsRegular() || fi.Mode()&fs.ModeSymlink != 0 {
				add(rel, abs, fi)
			}
			continue
		}
		_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			sub := rel
			if p != abs {
				sub = filepath.ToSlash(filepath.Join(rel, p[len(abs)+1:]))
				if rel == "." {
					sub = filepath.ToSlash(p[len(abs)+1:])
				}
			}
			if d.IsDir() {
				if p != abs && (excl.Match(sub, true) || (r.SkipSecrets && secrets.Match(sub, true))) {
					return fs.SkipDir
				}
				return nil
			}
			if excl.Match(sub, false) || (r.SkipSecrets && secrets.Match(sub, false) && !allow.Match(sub, false)) {
				return nil
			}
			if !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			if fi, err := d.Info(); err == nil {
				add(sub, p, fi)
			}
			return nil
		})
	}
	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].Path < res.Files[j].Path })
	return &protocol.Response{Stat: res}, nil
}
