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
