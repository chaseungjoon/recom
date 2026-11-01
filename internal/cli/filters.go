package cli

import (
	"regexp"
	"strings"

	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/pathmatch"
	"github.com/chaseungjoon/recom/internal/policy"
	"github.com/chaseungjoon/recom/internal/protocol"
)

// uploadRules builds the ordered rsync filter list (first match wins):
//
//  1. recom internals (.recom/) — never uploaded, not overridable
//  2. paths with uncollected remote outputs — protected so a stale local
//     copy cannot overwrite a newer remote-generated file
//  3. [sync] include — explicit opt-ins (can override secrets/defaults)
//  4. secret patterns (always, even with default_excludes = false)
//  5. built-in excludes (unless default_excludes = false)
//  6. toolchain build/environment directories, e.g. target/ for Rust
//     (unless default_excludes = false)
//  7. [sync] exclude
func uploadRules(p *config.Project, protected []string, toolchainExcludes ...string) []string {
	var r []string
	for _, x := range policy.Internal {
		r = append(r, "- "+x)
	}
	for _, x := range protected {
		r = append(r, "- "+pathmatch.EscapeLiteral(x))
	}
	for _, x := range p.Sync.Include {
		r = append(r, "+ "+x)
	}
	for _, x := range policy.SecretAllow {
		r = append(r, "+ "+x)
	}
	for _, x := range policy.Secrets {
		r = append(r, "- "+x)
	}
	if p.Sync.UseDefaultExcludes() {
		for _, x := range policy.UploadExcludes {
			r = append(r, "- "+x)
		}
		for _, x := range toolchainExcludes {
			r = append(r, "- "+x)
		}
	}
	for _, x := range p.Sync.Exclude {
		r = append(r, "- "+x)
	}
	return r
}

// outputPolicy converts project settings into the helper's policy;
// toolchainExcludes are the detected toolchains' build directories.
func outputPolicy(p *config.Project, toolchainExcludes ...string) protocol.OutputPolicy {
	maxFile, _ := p.MaxFileSize()
	maxRun, _ := p.MaxRunSize()
	return protocol.OutputPolicy{
		Exclude:     append(append([]string{}, p.Outputs.Exclude...), toolchainExcludes...),
		Include:     p.Outputs.Include,
		MaxFileSize: maxFile,
		MaxRunSize:  maxRun,
		HashLimit:   policy.DefaultHashLimit,
		SkipSecrets: true,
	}
}

var secretFlagRe = regexp.MustCompile(`(?i)(pass(word|wd)?|secret|token|api[-_]?key|access[-_]?key|auth|credential)`)

// displayCommand renders argv for logs and job listings, quoting arguments
// and redacting values that look like secrets.
func displayCommand(argv []string) string {
	out := make([]string, 0, len(argv))
	redactNext := false
	for i, a := range argv {
		switch {
		case redactNext:
			out = append(out, "***")
			redactNext = false
			continue
		case i > 0 && strings.HasPrefix(a, "-") && secretFlagRe.MatchString(a):
			if k, _, ok := strings.Cut(a, "="); ok {
				out = append(out, k+"=***")
				continue
			}
			redactNext = true
		case i > 0 && !strings.HasPrefix(a, "-") && strings.Contains(a, "="):
			if k, _, _ := strings.Cut(a, "="); secretFlagRe.MatchString(k) {
				out = append(out, k+"=***")
				continue
			}
		}
		out = append(out, quoteArg(a))
	}
	s := strings.Join(out, " ")
	if len(s) > 300 {
		s = s[:297] + "..."
	}
	return s
}

func quoteArg(a string) string {
	if a == "" {
		return "''"
	}
	if strings.IndexFunc(a, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@%+,", r))
	}) < 0 {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}
