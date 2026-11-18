// Package commits parses Conventional Commits and maps them to version bumps.
package commits

import "regexp"

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
	Scope    string
	Subject  string
	Breaking bool
	Bump     Bump
}

var header = regexp.MustCompile(`^(\w+)(?:\(([^)]*)\))?(!)?: (.+)`)
var breakingFooter = regexp.MustCompile(`(?m)^BREAKING[ -]CHANGE: `)

// Parse interprets a commit message. Non-conventional commits yield Bump None.
func Parse(subject, body string) Parsed {
	m := header.FindStringSubmatch(subject)
	if m == nil {
		return Parsed{Subject: subject}
	}
	p := Parsed{Type: m[1], Scope: m[2], Subject: m[4], Breaking: m[3] == "!" || breakingFooter.MatchString(body)}
	switch {
	case p.Breaking:
		p.Bump = Major
	case p.Type == "feat":
		p.Bump = Minor
	case p.Type == "fix" || p.Type == "perf":
		p.Bump = Patch
	}
	return p
}
