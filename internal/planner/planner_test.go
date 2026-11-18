package planner

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/re-dream-it/mono-release/internal/commits"
	"github.com/re-dream-it/mono-release/internal/config"
	"github.com/re-dream-it/mono-release/internal/git"
)

type fake struct {
	versions map[string]string   // component -> single existing tag version
	stable   map[string][]string // component -> stable tags that exist only on the stable branch (ref "main")
	extra    map[string][]string // component -> additional tag versions
	commits  []git.Commit
	messages map[string]string       // tag name -> message
	changed  map[string][]string     // base ref -> changed files
	since    map[string][]git.Commit // tag name -> commits after that tag (falls back to commits)
}

func (f fake) BranchRefs(string) []string { return nil }

func (f fake) Between(string, string) ([]git.Commit, error) { return nil, nil }
func (f fake) ChangedFiles(base string) ([]string, error)   { return f.changed[base], nil }
func (f fake) TagInfo(string) (string, time.Time, error)    { return "", time.Time{}, nil }

func (f fake) Tags(prefix, ref string) ([]git.Tag, error) {
	c := strings.TrimSuffix(prefix, "@") // component behind the prefix
	var vs []string
	if ref == "main" {
		vs = append(vs, f.stable[c]...)
	} else {
		if v, ok := f.versions[c]; ok {
			vs = append(vs, v)
		}
		vs = append(vs, f.extra[c]...)
	}
	var tags []git.Tag
	for _, v := range vs {
		tags = append(tags, git.Tag{Name: prefix + v, Version: semver.MustParse(v)})
	}
	return tags, nil
}
func (f fake) Commits(since string) ([]git.Commit, error) {
	if cs, ok := f.since[since]; ok {
		return cs, nil
	}
	return f.commits, nil
}
func (f fake) Head() (string, error)                  { return "abc", nil }
func (f fake) CreateTag(string, string, string) error { return nil }
func (f fake) TagMessage(tag string) (string, bool, error) {
	m, ok := f.messages[tag]
	return m, ok, nil
}
func (f fake) PushTags(string, []string) error { return nil }

const cfgYAML = `
components:
  orders:
    paths: [services/orders/**, shared/proto/orders/**]
    exclude: ["**/*.md"]
  billing:
    paths: [services/billing/**]
`

func TestBuild(t *testing.T) {
	cfg, err := config.Parse([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	f := fake{
		versions: map[string]string{"orders": "1.4.0"},
		commits: []git.Commit{
			{SHA: "1", Subject: "feat: proto change", Files: []string{"shared/proto/orders/a.proto"}},
			{SHA: "2", Subject: "fix: docs only", Files: []string{"services/orders/README.md"}},
			{SHA: "3", Subject: "fix: billing bug", Files: []string{"services/billing/x.java"}},
			{SHA: "4", Subject: "chore: deps", Files: []string{"services/orders/pom.xml"}},
		},
	}
	plan, err := Build(cfg, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range plan.Releases {
		got[r.Component] = r.Current + "->" + r.Next
	}
	want := map[string]string{"orders": "1.4.0->1.5.0", "billing": "->1.0.0"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected releases: %v", got)
	}
}

const cascadeYAML = `
components:
  lib:
    paths: [libs/**]
  orders:
    paths: [services/orders/**]
    depends_on: [lib]
  billing:
    paths: [services/billing/**]
    depends_on: [lib]
    cascade: minor
  audit:
    paths: [services/audit/**]
    depends_on: [lib]
    cascade: none
  fresh:
    paths: [services/fresh/**]
    depends_on: [lib]
umbrellas:
  platform:
    members: [orders, billing, lib]
`

func TestCascadeAndUmbrella(t *testing.T) {
	cfg, err := config.Parse([]byte(cascadeYAML))
	if err != nil {
		t.Fatal(err)
	}
	f := fake{
		versions: map[string]string{"lib": "2.0.0", "orders": "1.0.0", "billing": "3.1.0", "audit": "1.0.0", "platform": "1.2.0"},
		commits:  []git.Commit{{SHA: "1", Subject: "feat: lib feature", Files: []string{"libs/x.java"}}},
	}
	plan, err := Build(cfg, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range plan.Releases {
		got[r.Component] = r.Current + "->" + r.Next
	}
	want := map[string]string{
		"lib":     "2.0.0->2.1.0",
		"orders":  "1.0.0->1.0.1", // default cascade: patch
		"billing": "3.1.0->3.2.0", // cascade: minor
		// audit: cascade none; fresh: never released
	}
	if len(got) != len(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	if len(plan.Umbrellas) != 1 {
		t.Fatalf("umbrellas: %+v", plan.Umbrellas)
	}
	u := plan.Umbrellas[0]
	if u.Next != "1.3.0" || u.Bump != "minor" {
		t.Errorf("umbrella %s %s", u.Next, u.Bump)
	}
	if m := u.Manifest(); m != "billing@3.2.0\nlib@2.1.0\norders@1.0.1\n" {
		t.Errorf("manifest %q", m)
	}
}

func TestCascadeDoesNotDowngrade(t *testing.T) {
	cfg, _ := config.Parse([]byte(cascadeYAML))
	f := fake{
		versions: map[string]string{"lib": "1.0.0", "orders": "1.0.0", "billing": "1.0.0", "audit": "1.0.0"},
		commits: []git.Commit{
			{SHA: "1", Subject: "fix: lib", Files: []string{"libs/x"}},
			{SHA: "2", Subject: "feat!: orders", Files: []string{"services/orders/a"}},
		},
	}
	plan, _ := Build(cfg, f, Options{})
	for _, r := range plan.Releases {
		if r.Component == "orders" && r.Next != "2.0.0" {
			t.Errorf("orders: %s, want 2.0.0", r.Next)
		}
	}
}

func TestUmbrellaAll(t *testing.T) {
	cfg, err := config.Parse([]byte("components:\n a: {paths: [a/**]}\n b: {paths: [b/**]}\numbrellas:\n all: {all: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	f := fake{
		versions: map[string]string{"b": "2.0.0"},
		commits:  []git.Commit{{SHA: "1", Subject: "feat: a", Files: []string{"a/x"}}},
	}
	plan, _ := Build(cfg, f, Options{})
	if len(plan.Umbrellas) != 1 || plan.Umbrellas[0].Manifest() != "a@1.0.0\nb@2.0.0\n" {
		t.Errorf("%+v", plan.Umbrellas)
	}
}

const preYAML = `
branches:
  - {name: main}
  - {name: beta, prerelease: beta}
components:
  a: {paths: [a/**]}
`

func nextOf(t *testing.T, f fake, channel string) string {
	t.Helper()
	cfg, err := config.Parse([]byte(preYAML))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Build(cfg, f, Options{Channel: channel, StableRefs: []string{"main"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Releases) == 0 {
		return ""
	}
	return plan.Releases[0].Next
}

func TestPrerelease(t *testing.T) {
	feat := git.Commit{SHA: "f", Subject: "feat: x", Files: []string{"a/x"}}
	fix := git.Commit{SHA: "g", Subject: "fix: y", Files: []string{"a/y"}}

	// first prerelease off a stable version
	f := fake{versions: map[string]string{"a": "1.4.0"}, commits: []git.Commit{feat}}
	if got := nextOf(t, f, "beta"); got != "1.5.0-beta.1" {
		t.Errorf("first beta: %q", got)
	}
	// counter continues on the same line
	f = fake{
		versions: map[string]string{"a": "1.4.0"},
		extra:    map[string][]string{"a": {"1.5.0-beta.1"}},
		commits:  []git.Commit{fix, feat},
		since:    map[string][]git.Commit{"a@1.5.0-beta.1": {fix}},
	}
	if got := nextOf(t, f, "beta"); got != "1.5.0-beta.2" {
		t.Errorf("second beta: %q", got)
	}
	// nothing new since the last prerelease tag: no release
	f.since = map[string][]git.Commit{"a@1.5.0-beta.1": {}}
	if got := nextOf(t, f, "beta"); got != "" {
		t.Errorf("no new commits: %q", got)
	}
	// a breaking change bumps the line
	brk := git.Commit{SHA: "b", Subject: "feat!: y", Files: []string{"a/z"}}
	f = fake{
		versions: map[string]string{"a": "1.4.0"},
		extra:    map[string][]string{"a": {"1.5.0-beta.1"}},
		commits:  []git.Commit{brk, feat},
		since:    map[string][]git.Commit{"a@1.5.0-beta.1": {brk}},
	}
	if got := nextOf(t, f, "beta"); got != "2.0.0-beta.1" {
		t.Errorf("breaking: %q", got)
	}
	// a later, smaller change never moves the line backwards
	f = fake{
		versions: map[string]string{"a": "1.4.0"},
		extra:    map[string][]string{"a": {"2.0.0-beta.1"}},
		commits:  []git.Commit{fix},
		since:    map[string][]git.Commit{"a@2.0.0-beta.1": {fix}, "a@1.4.0": {fix}},
	}
	if got := nextOf(t, f, "beta"); got != "2.0.0-beta.2" {
		t.Errorf("no backwards: %q", got)
	}
	// stable ignores prerelease tags and releases the plain version
	f = fake{
		versions: map[string]string{"a": "1.4.0"},
		extra:    map[string][]string{"a": {"1.5.0-beta.2"}},
		commits:  []git.Commit{feat},
	}
	if got := nextOf(t, f, ""); got != "1.5.0" {
		t.Errorf("stable after beta: %q", got)
	}
	// once stable passes the beta line, the counter restarts
	f = fake{
		versions: map[string]string{"a": "1.5.0"},
		extra:    map[string][]string{"a": {"1.5.0-beta.2"}},
		commits:  []git.Commit{feat},
	}
	if got := nextOf(t, f, "beta"); got != "1.6.0-beta.1" {
		t.Errorf("after stable: %q", got)
	}
	// brand new component
	f = fake{commits: []git.Commit{feat}}
	if got := nextOf(t, f, "beta"); got != "1.0.0-beta.1" {
		t.Errorf("new component: %q", got)
	}
	// other channels are ignored
	f = fake{
		versions: map[string]string{"a": "1.4.0"},
		extra:    map[string][]string{"a": {"1.5.0-rc.3"}},
		commits:  []git.Commit{feat},
	}
	if got := nextOf(t, f, "beta"); got != "1.5.0-beta.1" {
		t.Errorf("other channel: %q", got)
	}
}

func TestStableBranchNotMergedBack(t *testing.T) {
	feat := git.Commit{SHA: "f", Subject: "feat: x", Files: []string{"a/x"}}
	// beta has 1.1.0-beta.1, main released 1.1.0 and was never merged into beta
	f := fake{
		versions: map[string]string{"a": "1.0.0"},
		extra:    map[string][]string{"a": {"1.1.0-beta.1"}},
		stable:   map[string][]string{"a": {"1.0.0", "1.1.0"}},
		commits:  []git.Commit{feat},
		since:    map[string][]git.Commit{"a@1.1.0": {feat}, "a@1.1.0-beta.1": {}},
	}
	// beta.1 is no longer the newest line; its commit is not yet in stable -> next line is 1.2.0-beta.1
	if got := nextOf(t, f, "beta"); got != "1.2.0-beta.1" {
		t.Errorf("stable moved on: %q", got)
	}
	// and without the stable branch's tags the old behaviour would be wrong
	f.stable = nil
	f.since = map[string][]git.Commit{"a@1.1.0-beta.1": {feat}}
	if got := nextOf(t, f, "beta"); got != "1.1.0-beta.2" {
		t.Errorf("sanity: %q", got)
	}
}

func TestLatestAndManifest(t *testing.T) {
	cfg, err := config.Parse([]byte(preYAML + "umbrellas:\n  platform: {all: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	f := fake{
		versions: map[string]string{"a": "1.4.0"},
		extra:    map[string][]string{"a": {"1.5.0-beta.2"}, "platform": {"2.0.0", "2.1.0-beta.1"}},
	}
	st, err := Latest(cfg, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Components["a"] != "1.4.0" || st.Umbrellas["platform"] != "2.0.0" {
		t.Errorf("stable: %+v", st)
	}
	be, _ := Latest(cfg, f, Options{Channel: "beta", StableRefs: []string{"main"}})
	if be.Components["a"] != "1.5.0-beta.2" || be.Umbrellas["platform"] != "2.1.0-beta.1" {
		t.Errorf("beta: %+v", be)
	}
	none, _ := Latest(cfg, fake{}, Options{})
	if len(none.Components) != 0 || len(none.Umbrellas) != 0 {
		t.Errorf("unreleased should be absent: %+v", none)
	}

	m, err := ParseManifest("billing@1.1.0\nlib@1.0.1-beta.2\n\n")
	if err != nil || m["billing"] != "1.1.0" || m["lib"] != "1.0.1-beta.2" || len(m) != 2 {
		t.Errorf("ParseManifest: %v %v", m, err)
	}
	for _, bad := range []string{"", "nonsense", "@1.0.0", "a@"} {
		if _, err := ParseManifest(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
	// round trip with Manifest()
	u := UmbrellaRelease{Members: map[string]string{"a": "1.0.0", "b": "2.0.0-rc.1"}}
	if back, err := ParseManifest(u.Manifest()); err != nil || !reflect.DeepEqual(back, u.Members) {
		t.Errorf("round trip: %v %v", back, err)
	}
}

// --stable on a prerelease branch must still see stable releases that live only on the stable branch.
func TestLatestStableSeesStableBranch(t *testing.T) {
	cfg, _ := config.Parse([]byte(preYAML))
	f := fake{
		versions: map[string]string{"a": "1.0.0"},
		extra:    map[string][]string{"a": {"1.1.0-beta.1"}},
		stable:   map[string][]string{"a": {"1.0.0", "1.1.0"}},
	}
	st, err := Latest(cfg, f, Options{StableRefs: []string{"main"}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Components["a"] != "1.1.0" {
		t.Errorf("got %q, want 1.1.0", st.Components["a"])
	}
}

func mustCfg(t *testing.T, y string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func releasesOf(p *Plan) map[string]string {
	m := map[string]string{}
	for _, r := range p.Releases {
		m[r.Component] = r.Current + "->" + r.Next
	}
	return m
}

func TestScopeIsAnExtraSignal(t *testing.T) {
	cfg := mustCfg(t, "components:\n orders: {paths: [services/orders/**], scopes: [api]}\n billing: {paths: [services/billing/**]}\n audit: {paths: [services/audit/**]}\n")
	f := fake{
		versions: map[string]string{"orders": "1.0.0", "billing": "1.0.0", "audit": "1.0.0"},
		commits: []git.Commit{
			{SHA: "1", Subject: "feat(api): new endpoint", Files: []string{"shared/gateway/x.yaml"}}, // alias scope, outside paths
			{SHA: "2", Subject: "fix(billing): rounding", Files: []string{"docs/notes.txt"}},         // own name as scope, outside paths
			{SHA: "3", Subject: "feat(unknown): nothing", Files: []string{"somewhere/else"}},         // scope that names nobody
			{SHA: "4", Subject: "feat: no scope at all", Files: []string{"somewhere/else"}},          // outside every path
			{SHA: "5", Subject: "feat(orders, audit): both", Files: []string{"services/billing/x"}},  // several scopes + another component's path
		},
	}
	plan, err := Build(cfg, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := releasesOf(plan)
	want := map[string]string{"orders": "1.0.0->1.1.0", "billing": "1.0.0->1.1.0", "audit": "1.0.0->1.1.0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// the scope alone is enough: without any file match billing's fix (patch) must not be lost,
	// and the stronger bump from the path-matching commit 5 must win
	if plan.Releases[1].Component != "billing" || plan.Releases[1].Bump != "minor" {
		t.Errorf("billing: %+v", plan.Releases[1])
	}
}

func TestSkipRelease(t *testing.T) {
	cfg := mustCfg(t, "components:\n a: {paths: [a/**]}\n b: {paths: [b/**]}\n")
	f := fake{
		versions: map[string]string{"a": "1.0.0", "b": "1.0.0"},
		commits: []git.Commit{
			{SHA: "1", Subject: "feat!: big thing [skip release]", Files: []string{"a/x"}},
			{SHA: "2", Subject: "fix: real fix", Body: "ok", Files: []string{"a/y"}},
			{SHA: "3", Subject: "feat: other", Body: "[skip release]", Files: []string{"b/x"}},
		},
	}
	plan, _ := Build(cfg, f, Options{})
	if got := releasesOf(plan); !reflect.DeepEqual(got, map[string]string{"a": "1.0.0->1.0.1"}) {
		t.Errorf("skip: %v (a: only the fix counts, b: nothing)", got)
	}
	for _, ch := range plan.Releases[0].Changes {
		if strings.Contains(ch.Subject, "skip release") {
			t.Errorf("skipped commit leaked into changes: %+v", ch)
		}
	}
}

func TestForce(t *testing.T) {
	cfg := mustCfg(t, "components:\n lib: {paths: [lib/**]}\n app: {paths: [app/**], depends_on: [lib]}\n other: {paths: [o/**]}\n")
	f := fake{versions: map[string]string{"lib": "1.0.0", "app": "2.0.0", "other": "3.0.0"}}

	plan, err := Build(cfg, f, Options{Force: map[string]commits.Bump{"lib": commits.Minor}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"lib": "1.0.0->1.1.0", "app": "2.0.0->2.0.1"} // forced, and its dependent follows by cascade
	if got := releasesOf(plan); !reflect.DeepEqual(got, want) {
		t.Errorf("force: %v, want %v", got, want)
	}
	if !plan.Releases[1].Forced || plan.Releases[0].Forced {
		t.Errorf("only lib is forced: %+v", plan.Releases)
	}
	// a force never lowers what the commits already ask for
	f.commits = []git.Commit{{SHA: "1", Subject: "feat!: x", Files: []string{"lib/a"}}}
	plan, _ = Build(cfg, f, Options{Force: map[string]commits.Bump{"lib": commits.Patch}})
	if got := releasesOf(plan)["lib"]; got != "1.0.0->2.0.0" {
		t.Errorf("force must not lower the bump: %s", got)
	}
	if _, err := Build(cfg, f, Options{Force: map[string]commits.Bump{"nope": commits.Patch}}); err == nil {
		t.Error("forcing an unknown component should fail")
	}
	// never released component: forced release is the initial version
	plan, _ = Build(cfg, fake{}, Options{Force: map[string]commits.Bump{"other": commits.Major}})
	if got := releasesOf(plan)["other"]; got != "->1.0.0" {
		t.Errorf("forced first release: %q", got)
	}
	// on a prerelease channel a forced release counts even though nothing is new since the last tag
	pf := fake{versions: map[string]string{"lib": "1.0.0"}, extra: map[string][]string{"lib": {"1.1.0-beta.1"}}, since: map[string][]git.Commit{"lib@1.1.0-beta.1": {}}}
	pcfg := mustCfg(t, "branches:\n - {name: main}\n - {name: beta, prerelease: beta}\ncomponents:\n lib: {paths: [lib/**]}\n")
	plan, _ = Build(pcfg, pf, Options{Channel: "beta", StableRefs: []string{"main"}, Force: map[string]commits.Bump{"lib": commits.Minor}})
	if got := releasesOf(plan)["lib"]; got != "1.1.0-beta.1->1.1.0-beta.2" {
		t.Errorf("forced on channel: %q", got)
	}
}

func TestExplain(t *testing.T) {
	cfg := mustCfg(t, `
components:
  lib: {paths: [lib/**]}
  app: {paths: [app/**], depends_on: [lib]}
  quiet: {paths: [q/**], depends_on: [lib], cascade: none}
  idle: {paths: [i/**]}
  chore: {paths: [c/**]}
  skipped: {paths: [s/**]}
umbrellas:
  platform: {all: true}
`)
	f := fake{
		versions: map[string]string{"lib": "1.0.0", "app": "1.0.0", "quiet": "1.0.0", "idle": "1.0.0", "chore": "1.0.0", "skipped": "1.0.0", "platform": "1.0.0"},
		commits: []git.Commit{
			{SHA: "a1", Subject: "feat: lib change", Files: []string{"lib/x"}},
			{SHA: "a2", Subject: "chore: tidy", Files: []string{"c/x"}},
			{SHA: "a3", Subject: "fix: nope [skip release]", Files: []string{"s/x"}},
		},
	}
	plan, err := Build(cfg, f, Options{Explain: true})
	if err != nil {
		t.Fatal(err)
	}
	d := plan.Details
	if d["lib"] == nil || !d["lib"].Released || d["lib"].Next != "1.1.0" || d["lib"].OwnBump != "minor" || len(d["lib"].Considered) != 1 || d["lib"].Considered[0].Via != "paths" {
		t.Errorf("lib: %+v", d["lib"])
	}
	if !d["app"].Released || d["app"].CascadeBump != "patch" || !reflect.DeepEqual(d["app"].CascadeFrom, []string{"lib"}) {
		t.Errorf("app: %+v", d["app"])
	}
	for name, mustContain := range map[string]string{
		"quiet":   "cascade is none",
		"idle":    "no commit since idle@1.0.0 touches idle",
		"chore":   "none is significant",
		"skipped": "[skip release]",
	} {
		if d[name].Released || !strings.Contains(d[name].Reason, mustContain) {
			t.Errorf("%s: released=%v reason=%q, want mention of %q", name, d[name].Released, d[name].Reason, mustContain)
		}
	}
	if u := d["platform"]; !u.Released || u.Kind != "umbrella" || !strings.Contains(u.Members["lib"], "1.0.0 -> 1.1.0") || !strings.Contains(u.Members["idle"], "unchanged") {
		t.Errorf("platform: %+v", u)
	}
	// without Explain no details are collected
	if p2, _ := Build(cfg, f, Options{}); p2.Details != nil {
		t.Error("Details must be empty unless requested")
	}
}

func TestChanged(t *testing.T) {
	cfg := mustCfg(t, `
components:
  lib: {paths: [lib/**]}
  app: {paths: [app/**], depends_on: [lib]}
  web: {paths: [web/**], depends_on: [app]}
  solo: {paths: [solo/**], scopes: [loner]}
`)
	f := fake{
		versions: map[string]string{"lib": "1.0.0", "app": "1.0.0", "web": "1.0.0", "solo": "1.0.0"},
		changed:  map[string][]string{"origin/main": {"lib/a.java", "README.md"}},
		commits: []git.Commit{
			{SHA: "1", Subject: "chore: not significant", Files: []string{"app/x"}},
			{SHA: "2", Subject: "docs(loner): by scope only", Files: []string{"elsewhere"}},
		},
	}
	byBase, err := Changed(cfg, f, ChangedOptions{Base: "origin/main"})
	if err != nil || !reflect.DeepEqual(byBase, []string{"lib"}) {
		t.Errorf("base diff: %v %v", byBase, err)
	}
	withDeps, _ := Changed(cfg, f, ChangedOptions{Base: "origin/main", WithDependents: true})
	if !reflect.DeepEqual(withDeps, []string{"app", "lib", "web"}) {
		t.Errorf("with dependents (transitive): %v", withDeps)
	}
	sinceRelease, _ := Changed(cfg, f, ChangedOptions{})
	if !reflect.DeepEqual(sinceRelease, []string{"app", "solo"}) {
		t.Errorf("since release (any commit, scope counts): %v", sinceRelease)
	}
}

func TestTagNames(t *testing.T) {
	cfg := mustCfg(t, "components:\n orders: {paths: [o/**]}\numbrellas:\n platform: {all: true}\n")
	f := fake{
		versions: map[string]string{"orders": "1.0.0"},
		commits:  []git.Commit{{SHA: "1", Subject: "feat: x", Files: []string{"o/a"}}},
	}
	plan, err := Build(cfg, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Releases[0].Tag != "orders@1.1.0" || plan.Umbrellas[0].Tag != "platform@1.0.0" {
		t.Errorf("tags: %s %s", plan.Releases[0].Tag, plan.Umbrellas[0].Tag)
	}
	if m := plan.Umbrellas[0].Manifest(); m != "orders@1.1.0\n" {
		t.Errorf("manifest: %q", m)
	}
}
