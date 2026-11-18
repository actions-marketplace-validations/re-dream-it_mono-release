package config

import "testing"

func TestValidate(t *testing.T) {
	bad := map[string]string{
		"cycle":       "components:\n a: {paths: [a], depends_on: [b]}\n b: {paths: [b], depends_on: [a]}\n",
		"unknown dep": "components:\n a: {paths: [a], depends_on: [x]}\n",
		"no paths":    "components:\n a: {}\n",
		"bad name":    "components:\n 'a@b': {paths: [a]}\n",
		"bad cascade": "components:\n a: {paths: [a], cascade: major}\n",
		"umb clash":   "components:\n a: {paths: [a]}\numbrellas:\n a: {members: [a]}\n",
		"umb member":  "components:\n a: {paths: [a]}\numbrellas:\n u: {members: [zz]}\n",
		"umb both":    "components:\n a: {paths: [a]}\numbrellas:\n u: {all: true, members: [a]}\n",
		"branch name": "branches:\n - {prerelease: beta}\ncomponents:\n a: {paths: [a]}\n",
		"bad channel": "branches:\n - {name: x, prerelease: 'be.ta'}\ncomponents:\n a: {paths: [a]}\n",
		"unknown key": "components:\n a: {paths: [a], pahts: [x]}\n",
	}
	for name, y := range bad {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := Parse([]byte("components:\n a: {paths: [a]}\n")); err != nil {
		t.Error(err)
	}
}

func TestChannelFor(t *testing.T) {
	c, err := Parse([]byte("branches:\n - {name: main}\n - {name: beta, prerelease: beta}\n - {name: 'rc/*', prerelease: rc}\ncomponents:\n a: {paths: [a]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for br, want := range map[string]string{"main": "", "beta": "beta", "rc/1.x": "rc"} {
		if got, err := c.ChannelFor(br); err != nil || got != want {
			t.Errorf("%s: %q %v", br, got, err)
		}
	}
	if _, err := c.ChannelFor("feature/x"); err == nil {
		t.Error("expected error for unlisted branch")
	}
	open, _ := Parse([]byte("components:\n a: {paths: [a]}\n"))
	if got, err := open.ChannelFor("anything"); err != nil || got != "" {
		t.Error("no branches configured should mean stable everywhere")
	}
}

func TestTagsHaveAFixedFormat(t *testing.T) {
	c, err := Parse([]byte("components:\n orders: {paths: [a]}\numbrellas:\n platform: {all: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.TagName("orders", "1.2.3"); got != "orders@1.2.3" {
		t.Errorf("component tag: %q", got)
	}
	if got := c.TagName("platform", "2.0.0-beta.1"); got != "platform@2.0.0-beta.1" {
		t.Errorf("umbrella tag: %q", got)
	}
	if c.TagPrefix("orders") != "orders@" {
		t.Errorf("prefix: %q", c.TagPrefix("orders"))
	}
	// the format is not configurable: an old config that sets it must fail loudly, not be ignored
	if _, err := Parse([]byte("tag_format: '{name}-v{version}'\ncomponents:\n a: {paths: [a]}\n")); err == nil {
		t.Error("tag_format is no longer a known key")
	}
}

func TestScopeMatchesAndReleases(t *testing.T) {
	c, err := Parse([]byte("components:\n orders: {paths: [a], scopes: [api, checkout]}\n billing: {paths: [b]}\numbrellas:\n platform: {all: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.ScopeMatches("orders", []string{"orders"}) || !c.ScopeMatches("orders", []string{"x", "checkout"}) {
		t.Error("name and alias should match")
	}
	if c.ScopeMatches("billing", []string{"api"}) || c.ScopeMatches("orders", []string{"billing"}) {
		t.Error("foreign scope must not match")
	}
	if u, comp := c.ReleaseTargets(); !u || comp {
		t.Errorf("default with umbrellas: umbrella=%v components=%v", u, comp)
	}
	noUmb, _ := Parse([]byte("components:\n a: {paths: [a]}\n"))
	if u, comp := noUmb.ReleaseTargets(); u || comp {
		t.Error("default without umbrellas must create nothing")
	}
	for setting, want := range map[string][2]bool{"all": {true, true}, "components": {false, true}, "none": {false, false}} {
		cc, _ := Parse([]byte("releases: " + setting + "\ncomponents:\n a: {paths: [a]}\n"))
		if u, comp := cc.ReleaseTargets(); [2]bool{u, comp} != want {
			t.Errorf("releases=%s: %v %v", setting, u, comp)
		}
	}
	if _, err := Parse([]byte("releases: sometimes\ncomponents:\n a: {paths: [a]}\n")); err == nil {
		t.Error("bad releases value should be rejected")
	}
}

func TestNotesValidation(t *testing.T) {
	base := "components:\n a: {paths: [a]}\numbrellas:\n platform: {all: true}\n"
	ok := []string{
		"notes:\n - {for: platform, file: CHANGELOG.md, history: all}\n",
		"notes:\n - {for: a, file: 'notes/{name}-{version}.md'}\n",
		"notes:\n - {for: '*', file: 'notes/{name}.md', history: all}\n",
	}
	for _, n := range ok {
		if _, err := Parse([]byte(n + base)); err != nil {
			t.Errorf("should be valid: %v\n%s", err, n)
		}
	}
	bad := []string{
		"notes:\n - {for: nope, file: x.md}\n",
		"notes:\n - {for: a}\n",
		"notes:\n - {for: a, file: /etc/passwd}\n",
		"notes:\n - {for: a, file: ../x.md}\n",
		"notes:\n - {for: a, file: x.md, history: weekly}\n",
		"notes:\n - {for: '*', file: all.md}\n",
		"notes:\n - {for: a, file: 'x-{version}.md', history: all}\n",
	}
	for _, n := range bad {
		if _, err := Parse([]byte(n + base)); err == nil {
			t.Errorf("should be rejected:\n%s", n)
		}
	}
}
