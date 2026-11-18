package main

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// version is the release version of the tool itself. Release builds set it:
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/monorelease
//
// When it is empty the version comes from the module metadata ("go install ...@v1.2.3"), and a
// build from a checkout reports "dev" together with the commit it was built from.
var version = ""

// pseudoVersion matches what the go command stamps on a build from a checkout:
// v0.0.0-20261118140723-94d4f2a66932, optionally followed by +dirty.
var pseudoVersion = regexp.MustCompile(`[-.]\d{14}-[0-9a-f]{12}(\+dirty)?$`)

func versionString() string {
	info, _ := debug.ReadBuildInfo()
	return formatVersion(version, info, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func formatVersion(ldflag string, info *debug.BuildInfo, goVersion, goos, goarch string) string {
	v := strings.TrimPrefix(ldflag, "v")
	commit, dirty := "", false
	if info != nil {
		if mv := info.Main.Version; v == "" && mv != "" && mv != "(devel)" && !pseudoVersion.MatchString(mv) {
			v = strings.TrimPrefix(mv, "v")
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				commit = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if v == "" {
		v = "dev"
	}
	var extra []string
	if commit != "" {
		if len(commit) > 7 {
			commit = commit[:7]
		}
		if dirty {
			commit += "-dirty"
		}
		extra = append(extra, "commit "+commit)
	}
	extra = append(extra, fmt.Sprintf("%s %s/%s", goVersion, goos, goarch))
	return fmt.Sprintf("monorelease %s (%s)", v, strings.Join(extra, ", "))
}
