package commits

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		subject, body string
		want          Bump
	}{
		{"feat: add x", "", Minor},
		{"feat(api): add x", "", Minor},
		{"fix: y", "", Patch},
		{"perf: z", "", Patch},
		{"chore: z", "", None},
		{"random message", "", None},
		{"feat!: drop old", "", Major},
		{"fix: y", "details\n\nBREAKING CHANGE: gone", Major},
	}
	for _, c := range cases {
		if got := Parse(c.subject, c.body).Bump; got != c.want {
			t.Errorf("Parse(%q) = %v, want %v", c.subject, got, c.want)
		}
	}
}

func TestScopes(t *testing.T) {
	cases := map[string][]string{
		"feat(orders): x":          {"orders"},
		"feat(orders, billing): x": {"orders", "billing"},
		"fix(orders,billing)!: x":  {"orders", "billing"},
		"feat: no scope":           nil,
		"feat(): empty":            nil,
		"not conventional (scope)": nil,
	}
	for subj, want := range cases {
		if got := Parse(subj, "").Scopes; !reflect.DeepEqual(got, want) {
			t.Errorf("%q: scopes %v, want %v", subj, got, want)
		}
	}
}

func TestSkipMarker(t *testing.T) {
	cases := []struct {
		subject, body string
		skip          bool
	}{
		{"feat: x [skip release]", "", true},
		{"feat: x", "details\n\n[Skip Release]", true},
		{"fix: x [release skip]", "", true},
		{"feat!: x [skip release]", "", true}, // even breaking changes are ignored
		{"feat: x [skip ci]", "", false},      // other tools' markers are not ours
		{"feat: skip release", "", false},
	}
	for _, c := range cases {
		p := Parse(c.subject, c.body)
		if p.Skip != c.skip {
			t.Errorf("%q: Skip=%v, want %v", c.subject, p.Skip, c.skip)
		}
		if c.skip && p.Bump != None {
			t.Errorf("%q: a skipped commit must not bump, got %v", c.subject, p.Bump)
		}
	}
}
