package pathmatch

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pat   string
		path  string
		isDir bool
		want  bool
	}{
		{"*.pyc", "a/b/c.pyc", false, true},
		{"*.pyc", "c.pyc", false, true},
		{"*.pyc", "c.py", false, false},
		{".git/", ".git", true, true},
		{".git/", "sub/.git", true, true},
		{".git/", ".git", false, false}, // dir-only
		{"/build/", "build", true, true},
		{"/build/", "src/build", true, false}, // anchored
		{"data/raw", "data/raw", true, true},
		{"data/raw", "x/data/raw", true, true}, // unanchored with slash: suffix match
		{"data/raw", "xdata/raw", true, false},
		{"**/*.log", "a/b/c.log", false, true},
		{"logs/**", "logs/a/b", false, true},
		{"file?.txt", "file1.txt", false, true},
		{"file?.txt", "file10.txt", false, false},
		{"[ab].txt", "a.txt", false, true},
		{"[!ab].txt", "a.txt", false, false},
		{"[!ab].txt", "c.txt", false, true},
		{"*", "a/b", false, true},
		{"a*b", "a/b", false, false}, // * does not cross /
	}
	for _, c := range cases {
		p, err := Compile(c.pat)
		if err != nil {
			t.Fatalf("%q: %v", c.pat, err)
		}
		if got := p.Match(c.path, c.isDir); got != c.want {
			t.Errorf("Match(%q, %q, dir=%v) = %v, want %v", c.pat, c.path, c.isDir, got, c.want)
		}
	}
}

func TestMatchTree(t *testing.T) {
	s := MustCompileSet([]string{"node_modules/", ".env"})
	if !s.MatchTree("a/node_modules/x/y.js", false) {
		t.Error("file under excluded dir should match")
	}
	if s.MatchTree("a/node_modulesx/y.js", false) {
		t.Error("prefix of a component must not match")
	}
	if !s.MatchTree("cfg/.env", false) {
		t.Error("basename match")
	}
}

func TestCompileErrors(t *testing.T) {
	for _, p := range []string{"", "   ", "/", "[abc"} {
		if _, err := Compile(p); err == nil {
			t.Errorf("Compile(%q) should fail", p)
		}
	}
}

func TestEscapeLiteral(t *testing.T) {
	cases := map[string]string{
		"plain/file.txt": "/plain/file.txt",
		"star*.txt":      `/star\*.txt`,
		"q?[x]":          `/q\?\[x]`,
		`back\slash*`:    `/back\\slash\*`,
	}
	for in, want := range cases {
		if got := EscapeLiteral(in); got != want {
			t.Errorf("EscapeLiteral(%q) = %q, want %q", in, got, want)
		}
		// The escaped pattern must match exactly the literal path.
		p, err := Compile(EscapeLiteral(in))
		if err != nil {
			t.Fatal(err)
		}
		if !p.Match(in, false) {
			t.Errorf("escaped %q does not match itself", in)
		}
	}
	p, _ := Compile(EscapeLiteral("star*.txt"))
	if p.Match("starXYZ.txt", false) {
		t.Error("escaped wildcard still acts as a wildcard")
	}
}

func FuzzCompile(f *testing.F) {
	for _, s := range []string{"*.py", "a/**/b", "[x", "/a/", `\*`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, pat string) {
		p, err := Compile(pat)
		if err == nil {
			_ = p.Match("a/b/c", false)
		}
	})
}
