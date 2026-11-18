// Package commits parses Conventional Commits and maps them to version bumps.
package commits

import (
	"regexp"
	"strings"
)

type Bump int

const (
	None Bump = iota
	Patch
	Minor
	Major
)

func (b Bump) String() string {
	return [...]string{"none", "patch", "minor", "major"}[b]
}

type Parsed struct {
	Type     string
	Scope    string   // raw text between the parentheses
	Scopes   []string // Scope split on commas: "feat(orders, billing)" -> [orders billing]
	Subject  string
	Breaking bool
	// Skip is set when the commit asks to be ignored: "[skip release]" in its message.
	// A skipped commit never causes or raises a release.
	Skip bool
	Bump Bump
}

var header = regexp.MustCompile(`^(\w+)(?:\(([^)]*)\))?(!)?: (.+)`)
var skipMarker = regexp.MustCompile(`(?i)\[(skip release|release skip)\]`)
var breakingFooter = regexp.MustCompile(`(?m)^BREAKING[ -]CHANGE: `)

// Parse interprets a commit message. Non-conventional commits yield Bump None.
func Parse(subject, body string) Parsed {
	skip := skipMarker.MatchString(subject) || skipMarker.MatchString(body)
	m := header.FindStringSubmatch(subject)
	if m == nil {
		return Parsed{Subject: subject, Skip: skip}
	}
	p := Parsed{Type: m[1], Scope: m[2], Subject: m[4], Breaking: m[3] == "!" || breakingFooter.MatchString(body), Skip: skip}
	for _, sc := range strings.Split(m[2], ",") {
		if sc = strings.TrimSpace(sc); sc != "" {
			p.Scopes = append(p.Scopes, sc)
		}
	}
	switch {
	case p.Breaking:
		p.Bump = Major
	case p.Type == "feat":
		p.Bump = Minor
	case p.Type == "fix" || p.Type == "perf":
		p.Bump = Patch
	}
	if skip {
		p.Bump = None
	}
	return p
}
