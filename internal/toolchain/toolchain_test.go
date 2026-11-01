package toolchain

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(plans []Plan) string {
	var n []string
	for _, p := range plans {
		n = append(n, p.Name)
	}
	return strings.Join(n, ",")
}

func TestDetectPythonStrategies(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		summary string // substring; "" = not detected
	}{
		{"requirements", map[string]string{"requirements.txt": "numpy\n"}, "pip install -r requirements.txt"},
		{"uv lock", map[string]string{"pyproject.toml": "[project]\nname='x'\n", "uv.lock": ""}, "uv sync"},
		{"pyproject project", map[string]string{"pyproject.toml": "[project]\nname='x'\ndependencies=['rich']\n"}, "uv sync"},
		{"requirements beat a pyproject", map[string]string{"pyproject.toml": "[project]\nname='x'\n", "requirements.txt": "rich\n"}, "requirements.txt"},
		{"poetry", map[string]string{"pyproject.toml": "[tool.poetry]\nname='x'\n", "poetry.lock": ""}, "poetry install"},
		{"pipenv", map[string]string{"Pipfile": ""}, "pipenv install"},
		{"setup.py", map[string]string{"setup.py": ""}, "pip install -e ."},
		{"tool-only pyproject is not an environment", map[string]string{"pyproject.toml": "[tool.ruff]\nline-length = 100\n"}, ""},
		{"nothing", map[string]string{"main.py": "print(1)\n"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			for f, s := range c.files {
				write(t, root, f, s)
			}
			plans := Detect(root)
			if c.summary == "" {
				if len(plans) != 0 {
					t.Fatalf("detected %s", names(plans))
				}
				return
			}
			if len(plans) != 1 || plans[0].Name != "python" {
				t.Fatalf("plans = %s", names(plans))
			}
			p := plans[0]
			if !strings.Contains(p.Summary, c.summary) {
				t.Fatalf("summary %q, want %q", p.Summary, c.summary)
			}
			if p.Requires != ".venv/bin/python" || p.Path[0] != ".venv/bin" || p.RelEnv["VIRTUAL_ENV"] != ".venv" {
				t.Fatalf("python plan wiring: %+v", p)
			}
		})
	}
}

func TestPipFallbackInstallsPyprojectDependencies(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pyproject.toml", "[project]\nname='x'\ndependencies=['rich>=13', \"it's\"]\n[dependency-groups]\ndev=['pytest', {include-group='x'}]\n")
	p := Detect(root)[0]
	if !strings.Contains(p.Script, `pipi 'rich>=13' 'it'\''s' pytest`) {
		t.Fatalf("pip fallback does not install the declared dependencies:\n%s", p.Script)
	}
}

func TestPythonVersionFileSelectsInterpreter(t *testing.T) {
	root := t.TempDir()
	write(t, root, "requirements.txt", "")
	write(t, root, ".python-version", "3.11.4\n")
	if p := Detect(root)[0]; !strings.Contains(p.Script, "PYS=python3.11\n") {
		t.Fatalf("script:\n%s", p.Script)
	}
}

func TestCondaReplacesPythonAndOrderIsStable(t *testing.T) {
	root := t.TempDir()
	write(t, root, "environment.yml", "dependencies: [python=3.11]\n")
	write(t, root, "requirements.txt", "")
	write(t, root, "package.json", `{"packageManager":"pnpm@9.1.0"}`)
	write(t, root, "Cargo.toml", "")
	plans := Detect(root)
	if got := names(plans); got != "conda,node,rust" {
		t.Fatalf("plans = %s", got)
	}
	if !strings.Contains(plans[1].Script, "pnpm") || !strings.Contains(plans[1].Script, "corepack pnpm") {
		t.Fatalf("packageManager not honored:\n%s", plans[1].Script)
	}
	if plans[2].SyncExclude[0] != "/target/" || plans[2].OutputExclude[0] != "/target/" {
		t.Fatalf("rust excludes: %+v", plans[2])
	}
}

func TestNodePackageManagerFromLockfile(t *testing.T) {
	for lock, want := range map[string]string{"": "npm install", "yarn.lock": "run_pm install", "bun.lock": "bun install", "pnpm-lock.yaml": "confirmModulesPurge"} {
		root := t.TempDir()
		write(t, root, "package.json", "{}")
		if lock != "" {
			write(t, root, lock, "")
		}
		p := Detect(root)[0]
		if !strings.Contains(p.Script, want) {
			t.Errorf("%q: script lacks %q:\n%s", lock, want, p.Script)
		}
	}
}

func TestResolveConfig(t *testing.T) {
	root := t.TempDir()
	write(t, root, "requirements.txt", "")
	write(t, root, "go.mod", "module x\n")

	if got := names(Resolve(root, Config{Toolchains: []string{}}).Plans); got != "" {
		t.Fatalf("[] must disable setup, got %s", got)
	}
	res := Resolve(root, Config{Toolchains: []string{"python", "rust"}})
	if got := names(res.Plans); got != "python" {
		t.Fatalf("explicit list: %s", got)
	}
	hints := strings.Join(res.Hints, "\n")
	if !strings.Contains(hints, `"rust"`) || !strings.Contains(hints, `"go" is not in [setup]`) {
		t.Fatalf("hints: %s", hints)
	}
	res = Resolve(root, Config{Toolchains: []string{"auto"}, Command: "make deps", Watch: []string{"*.mod"}})
	if got := names(res.Plans); got != "python,go,custom" {
		t.Fatalf("auto + command: %s", got)
	}
	if c := res.Plans[2]; c.Watch[0] != "go.mod" || !strings.Contains(c.Script, `-c 'make deps'`) {
		t.Fatalf("custom plan: %+v", c)
	}
}

func TestFingerprintTracksDependencyFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "requirements.txt", "numpy\n")
	write(t, root, "main.py", "print(1)\n")
	fp := Detect(root)[0].Fingerprint
	write(t, root, "main.py", "print(2)\n")
	if Detect(root)[0].Fingerprint != fp {
		t.Fatal("source change must not trigger a new setup")
	}
	write(t, root, "requirements.txt", "numpy\ntorch\n")
	if Detect(root)[0].Fingerprint == fp {
		t.Fatal("dependency change must trigger a new setup")
	}
	write(t, root, "requirements-dev.txt", "pytest\n")
	fp2 := Detect(root)[0].Fingerprint
	write(t, root, "requirements-dev.txt", "pytest\nruff\n")
	if Detect(root)[0].Fingerprint == fp2 {
		t.Fatal("included requirements files are watched too")
	}
}

// Every generated script must at least be valid POSIX sh.
func TestScriptsParse(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	root := t.TempDir()
	for f, s := range map[string]string{
		"environment.yml": "", "requirements.txt": "", "package.json": "{}", "Cargo.toml": "", "go.mod": "",
		"pom.xml": "", "build.gradle": "", "Gemfile": "", "composer.json": "", "Project.toml": "", "renv.lock": "",
		"app.csproj": "", "mix.exs": "",
	} {
		write(t, root, f, s)
	}
	plans := Resolve(root, Config{Toolchains: append([]string{"python"}, Names()...), Command: `echo "it's" | tr a b`}).Plans
	if len(plans) != len(Names())+1 {
		t.Fatalf("expected every toolchain plus custom, got %s", names(plans))
	}
	for _, p := range plans {
		if out, err := exec.Command(sh, "-n", "-c", p.FullScript()).CombinedOutput(); err != nil {
			t.Errorf("%s: invalid sh: %v\n%s\n%s", p.Name, err, out, p.FullScript())
		}
	}
}

// The python strategy end to end with a stub uv/pip-less interpreter is
// covered by the e2e tests; here: the stamp is only written on success.
func TestFullScriptStampsOnlyOnSuccess(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	ok := Plan{Name: "custom", Script: "true\n", Fingerprint: "abc", Requires: ""}
	if out, err := exec.Command(sh, "-c", "cd "+Quote(dir)+" && "+ok.FullScript()).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, StampDir, "custom"))
	if err != nil || string(b) != ok.Stamp() {
		t.Fatalf("stamp %q, %v", b, err)
	}
	bad := Plan{Name: "python", Script: "false\n", Fingerprint: "def"}
	if err := exec.Command(sh, "-c", "cd "+Quote(dir)+" && "+bad.FullScript()).Run(); err == nil {
		t.Fatal("failing setup must fail")
	}
	if _, err := os.Stat(filepath.Join(dir, StampDir, "python")); err == nil {
		t.Fatal("failed setup left a stamp")
	}
}
