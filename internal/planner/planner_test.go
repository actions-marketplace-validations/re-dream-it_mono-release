package planner

import (
	"reflect"
	"testing"

	"github.com/Masterminds/semver/v3"

	"mono-release/internal/config"
	"mono-release/internal/git"
)

type fake struct {
	versions map[string]string   // component -> single existing tag version
	stable   map[string][]string // component -> stable tags that exist only on the stable branch (ref "main")
	extra    map[string][]string // component -> additional tag versions
	commits  []git.Commit
	messages map[string]string       // tag name -> message
	since    map[string][]git.Commit // tag name -> commits after that tag (falls back to commits)
}

func (f fake) BranchRefs(string) []string { return nil }

func (f fake) Tags(c, ref string) ([]git.Tag, error) {
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
		tags = append(tags, git.Tag{Name: c + "@" + v, Version: semver.MustParse(v)})
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
