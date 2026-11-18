package commits

import "testing"

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
