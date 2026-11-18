package main

import (
	"runtime/debug"
	"testing"
)

func TestFormatVersion(t *testing.T) {
	vcs := func(rev string, modified bool) *debug.BuildInfo {
		m := "false"
		if modified {
			m = "true"
		}
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: rev}, {Key: "vcs.modified", Value: m}}}
	}
	cases := []struct {
		name   string
		ldflag string
		info   *debug.BuildInfo
		want   string
	}{
		{"release build via ldflags", "1.2.3", nil, "monorelease 1.2.3 (go1.27 linux/amd64)"},
		{"v prefix is dropped", "v1.2.3", nil, "monorelease 1.2.3 (go1.27 linux/amd64)"},
		{"ldflags win over module metadata", "2.0.0", &debug.BuildInfo{Main: debug.Module{Version: "v1.0.0"}}, "monorelease 2.0.0 (go1.27 linux/amd64)"},
		{"go install @v1.4.0", "", &debug.BuildInfo{Main: debug.Module{Version: "v1.4.0"}}, "monorelease 1.4.0 (go1.27 linux/amd64)"},
		{"pseudo-version of a checkout build", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261118140723-94d4f2a66932+dirty"}}, "monorelease dev (go1.27 linux/amd64)"},
		{"pseudo-version after a tag", "", &debug.BuildInfo{Main: debug.Module{Version: "v1.2.4-0.20261118140723-94d4f2a66932"}}, "monorelease dev (go1.27 linux/amd64)"},
		{"checkout build", "", vcs("0123456789abcdef", false), "monorelease dev (commit 0123456, go1.27 linux/amd64)"},
		{"checkout build with local changes", "", vcs("0123456789abcdef", true), "monorelease dev (commit 0123456-dirty, go1.27 linux/amd64)"},
		{"no information at all", "", nil, "monorelease dev (go1.27 linux/amd64)"},
		{"release build also shows the commit", "1.2.3", vcs("abcdef0123", false), "monorelease 1.2.3 (commit abcdef0, go1.27 linux/amd64)"},
	}
	for _, c := range cases {
		if got := formatVersion(c.ldflag, c.info, "go1.27", "linux", "amd64"); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
