// Package pathmatch implements the rsync-style path patterns used by recom
// for upload filters and output policies, so that the client (which hands
// patterns to rsync) and the helper (which evaluates them itself) agree.
//
// Supported syntax, mirroring rsync's include/exclude rules:
//
//   - A trailing "/" matches directories only.
//   - A leading "/" anchors the pattern at the project root.
//   - A pattern containing "/" (other than a trailing one) or "**" is matched
//     against the full relative path (at a component boundary when not anchored);
//     otherwise it is matched against the final path component.
//   - "*" matches any run of characters except "/", "**" matches anything,
//     "?" matches one non-"/" character and "[...]" is a character class.
package pathmatch

import (
	"fmt"
	"regexp"
	"strings"
)

// Pattern is one compiled rule.
type Pattern struct {
	Source  string
	dirOnly bool
	full    bool
	re      *regexp.Regexp
}

// Compile compiles an rsync-style pattern.
func Compile(p string) (*Pattern, error) {
	src := p
	if strings.TrimSpace(p) == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	pat := &Pattern{Source: src}
	if strings.HasSuffix(p, "/") {
		pat.dirOnly = true
		p = strings.TrimRight(p, "/")
		if p == "" {
			return nil, fmt.Errorf("pattern %q matches everything", src)
		}
	}
	anchored := strings.HasPrefix(p, "/")
	p = strings.TrimLeft(p, "/")
	pat.full = anchored || strings.Contains(p, "/") || strings.Contains(p, "**")
	body, err := translate(p)
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", src, err)
	}
	switch {
	case anchored:
		body = "^" + body + "$"
	case pat.full:
		body = "^(?:.*/)?" + body + "$"
	default:
		body = "^" + body + "$"
	}
	re, err := regexp.Compile(body)
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", src, err)
	}
	pat.re = re
	return pat, nil
}

func translate(p string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch c {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			j := strings.IndexByte(p[i+1:], ']')
			if j < 0 {
				return "", fmt.Errorf("unterminated character class")
			}
			class := p[i+1 : i+1+j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += j + 1
		case '\\':
			if i+1 < len(p) {
				i++
				b.WriteString(regexp.QuoteMeta(string(p[i])))
			} else {
				b.WriteString(`\\`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String(), nil
}

// Match reports whether rel (slash-separated, relative) matches the pattern.
func (p *Pattern) Match(rel string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	if p.full {
		return p.re.MatchString(rel)
	}
	base := rel
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		base = rel[i+1:]
	}
	return p.re.MatchString(base)
}

// Set is an ordered list of patterns.
type Set []*Pattern

// CompileSet compiles a list of patterns.
func CompileSet(patterns []string) (Set, error) {
	s := make(Set, 0, len(patterns))
	for _, p := range patterns {
		c, err := Compile(p)
		if err != nil {
			return nil, err
		}
		s = append(s, c)
	}
	return s, nil
}

// MustCompileSet is CompileSet for built-in pattern lists.
func MustCompileSet(patterns []string) Set {
	s, err := CompileSet(patterns)
	if err != nil {
		panic(err)
	}
	return s
}

// Match reports whether any pattern matches rel itself.
func (s Set) Match(rel string, isDir bool) bool {
	for _, p := range s {
		if p.Match(rel, isDir) {
			return true
		}
	}
	return false
}

// MatchTree reports whether rel or any of its ancestor directories matches.
func (s Set) MatchTree(rel string, isDir bool) bool {
	if len(s) == 0 {
		return false
	}
	if s.Match(rel, isDir) {
		return true
	}
	for i := 0; i < len(rel); i++ {
		if rel[i] == '/' && s.Match(rel[:i], true) {
			return true
		}
	}
	return false
}

// EscapeLiteral turns a literal relative path into an anchored rsync pattern
// that matches exactly that path.
func EscapeLiteral(rel string) string {
	if strings.ContainsAny(rel, "*?[") {
		var b strings.Builder
		for _, r := range rel {
			if strings.ContainsRune(`*?[\`, r) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		rel = b.String()
	}
	return "/" + rel
}
