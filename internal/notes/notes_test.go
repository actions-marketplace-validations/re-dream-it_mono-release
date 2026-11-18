package notes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/re-dream-it/mono-release/internal/config"
	"github.com/re-dream-it/mono-release/internal/git"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file, msg string) {
	t.Helper()
	if file != "" {
		p := filepath.Join(dir, file)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(msg), 0o644)
		run(t, dir, "add", "-A")
	}
	run(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
}

func tag(t *testing.T, dir, name, msg string) {
	t.Helper()
	if msg == "" {
		msg = name
	}
	run(t, dir, "tag", "-a", name, "-m", msg)
}

const cfgYAML = `
components:
  lib: {paths: [libs/**]}
  orders: {paths: [services/orders/**], depends_on: [lib], scopes: [api]}
  billing: {paths: [services/billing/**]}
umbrellas:
  platform: {all: true}
`

// fixture builds a small history with component and umbrella releases and returns the generator.
func fixture(t *testing.T, repoURL string) (*Generator, string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "t@t")
	run(t, dir, "config", "user.name", "t")

	commit(t, dir, "services/orders/a", "feat(orders): add endpoint")
	commit(t, dir, "services/billing/a", "feat: invoices")
	tag(t, dir, "orders@1.0.0", "")
	tag(t, dir, "billing@1.0.0", "")
	tag(t, dir, "lib@1.0.0", "")
	tag(t, dir, "platform@1.0.0", "billing@1.0.0\nlib@1.0.0\norders@1.0.0\n")

	commit(t, dir, "services/orders/b", "fix: orders null pointer")
	commit(t, dir, "services/billing/b", "feat!: billing rewrite")
	commit(t, dir, "services/orders/c", "chore: tidy orders")
	commit(t, dir, "", "Merge branch 'x' into 'main'")
	commit(t, dir, "services/orders/d", "fix: hidden [skip release]")
	commit(t, dir, "docs/api.md", "feat(api): documented via scope alias")
	tag(t, dir, "orders@1.1.0", "")
	tag(t, dir, "billing@2.0.0", "")
	tag(t, dir, "lib@1.0.1", "") // released only because of a dependency in real life: here nothing touched libs/
	tag(t, dir, "platform@1.1.0", "billing@2.0.0\nlib@1.0.1\norders@1.1.0\n")

	cfg, err := config.Parse([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	return &Generator{Cfg: cfg, Git: git.New(dir), RepoURL: repoURL}, dir
}

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func mustNotContain(t *testing.T, got string, nots ...string) {
	t.Helper()
	for _, n := range nots {
		if strings.Contains(got, n) {
			t.Errorf("unexpected %q in:\n%s", n, got)
		}
	}
}

func TestComponentNotes(t *testing.T) {
	g, _ := fixture(t, "https://github.com/acme/mono")
	md, err := g.Notes("orders", "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, md,
		"## orders 1.1.0 (",
		"**Bug fixes**", "orders null pointer", "](https://github.com/acme/mono/commit/",
		"Other changes (1)", "tidy orders",
		"documented via scope alias", // counted through the "api" scope alias although docs/ is outside the component's paths
		"[Full diff](https://github.com/acme/mono/compare/orders@1.0.0...orders@1.1.0)",
	)
	mustNotContain(t, md, "billing rewrite", "invoices", "hidden", "Merge branch")

	b, _ := g.Notes("billing", "2.0.0")
	mustContain(t, b, "**Breaking changes**", "billing rewrite")
	mustNotContain(t, b, "orders null pointer")

	// a component whose release brought no commits of its own says so
	l, err := g.Notes("lib", "1.0.1")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, l, "released because a dependency changed")

	// the first release lists all history of the component and has no diff link
	first, _ := g.Notes("orders", "1.0.0")
	mustContain(t, first, "add endpoint")
	mustNotContain(t, first, "Full diff")
}

func TestLatestIsDefault(t *testing.T) {
	g, _ := fixture(t, "")
	md, err := g.Notes("orders", "")
	if err != nil || !strings.Contains(md, "orders 1.1.0") {
		t.Errorf("latest: %v\n%s", err, md)
	}
	mustContain(t, md, "(") // short sha without a link when the repository URL is unknown
	mustNotContain(t, md, "](http", "Full diff")
}

func TestUmbrellaNotes(t *testing.T) {
	g, _ := fixture(t, "https://gitlab.example.com/group/proj")
	md, err := g.Notes("platform", "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, md,
		"## platform 1.1.0 (",
		"| orders | 1.0.0 → 1.1.0 |",
		"| billing | 1.0.0 → 2.0.0 |",
		"| lib | 1.0.0 → 1.0.1 |",
		"### billing 1.0.0 → 2.0.0", "billing rewrite",
		"### orders 1.0.0 → 1.1.0", "orders null pointer",
		"/-/commit/", // GitLab style links
		"[Full diff](https://gitlab.example.com/group/proj/-/compare/platform@1.0.0...platform@1.1.0)",
	)
	first, _ := g.Notes("platform", "1.0.0")
	mustContain(t, first, "| orders | 1.0.0 |", "| billing | 1.0.0 |")
	mustNotContain(t, first, "→", "###") // nothing to compare with yet
}

func TestChangelogAndErrors(t *testing.T) {
	g, _ := fixture(t, "")
	md, err := g.Changelog("platform")
	if err != nil {
		t.Fatal(err)
	}
	if i, j := strings.Index(md, "platform 1.1.0"), strings.Index(md, "platform 1.0.0"); i < 0 || j < 0 || i > j {
		t.Errorf("newest release must come first:\n%s", md)
	}
	for _, tc := range [][2]string{{"nope", ""}, {"orders", "9.9.9"}} {
		if _, err := g.Notes(tc[0], tc[1]); err == nil {
			t.Errorf("Notes(%q,%q) should fail", tc[0], tc[1])
		}
	}
	if _, err := g.Notes("lib", "0.0.1"); err == nil {
		t.Error("unknown version should fail")
	}
}

func TestWriteConfiguredFiles(t *testing.T) {
	g, dir := fixture(t, "")
	g.Cfg.Notes = []config.NotesEntry{
		{For: "platform", File: "release-notes/{name}-{version}.md"},
		{For: "*", File: "changelogs/{name}.md", History: "all"},
	}
	written, err := g.Write(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"release-notes/platform-1.1.0.md", "changelogs/orders.md", "changelogs/platform.md", "changelogs/billing.md", "changelogs/lib.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written (written: %v)", f, written)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "release-notes/platform-1.1.0.md"))
	mustContain(t, string(data), "platform 1.1.0")
	// restricting to one name writes only that one
	os.RemoveAll(filepath.Join(dir, "changelogs"))
	if _, err := g.Write(dir, "orders"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "changelogs/billing.md")); err == nil {
		t.Error("only orders should be written")
	}
}

func TestLinkStyle(t *testing.T) {
	cases := []struct {
		repo, style, wantCommit string
	}{
		{"http://localhost:8929/g/p", "gitlab", "/-/commit/"}, // host name alone cannot tell: the style is given
		{"http://localhost:8929/g/p", "", "/commit/"},
		{"https://gitlab.com/g/p", "", "/-/commit/"},
		{"https://gitlab.com/g/p", "github", "/commit/"}, // an explicit style wins over the guess
		{"https://github.com/o/r", "", "/commit/"},
	}
	for _, c := range cases {
		g := &Generator{RepoURL: c.repo, Style: c.style}
		if got := g.commitPath(); got != c.wantCommit {
			t.Errorf("%+v: %s, want %s", c, got, c.wantCommit)
		}
	}
}

func TestWebURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/mono.git":                       "https://github.com/acme/mono",
		"https://github.com/acme/mono":                           "https://github.com/acme/mono",
		"git@github.com:acme/mono.git":                           "https://github.com/acme/mono",
		"https://oauth2:SECRET-TOKEN@gitlab.com/group/sub/p.git": "https://gitlab.com/group/sub/p",
		"https://user@bitbucket.org/acme/mono.git/":              "https://bitbucket.org/acme/mono",
		"ssh://git@gitlab.example.com:2222/g/p.git":              "https://gitlab.example.com/g/p",
		"http://localhost:8929/root/monorepo.git":                "http://localhost:8929/root/monorepo",
		"":                 "",
		"/some/local/path": "",
	}
	for in, want := range cases {
		got := WebURL(in)
		if got != want {
			t.Errorf("WebURL(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(got, "SECRET") {
			t.Errorf("credentials leaked: %q", got)
		}
	}
}

// The notes of a stable version cover everything since the previous STABLE version (including the work
// done in its pre-releases); the notes of a pre-release cover only what is new since the previous version.
func TestPrereleaseNotes(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "t@t")
	run(t, dir, "config", "user.name", "t")
	commit(t, dir, "services/orders/a", "feat: first")
	tag(t, dir, "orders@1.0.0", "")
	commit(t, dir, "services/orders/b", "feat: in beta one")
	tag(t, dir, "orders@1.1.0-beta.1", "")
	commit(t, dir, "services/orders/c", "fix: in beta two")
	tag(t, dir, "orders@1.1.0-beta.2", "")
	commit(t, dir, "services/orders/d", "fix: after the last beta")
	tag(t, dir, "orders@1.1.0", "")

	cfg, err := config.Parse([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	g := &Generator{Cfg: cfg, Git: git.New(dir)}

	stable, err := g.Notes("orders", "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, stable, "in beta one", "in beta two", "after the last beta")
	mustNotContain(t, stable, "first")

	beta2, _ := g.Notes("orders", "1.1.0-beta.2")
	mustContain(t, beta2, "in beta two")
	mustNotContain(t, beta2, "in beta one", "after the last beta")

	beta1, _ := g.Notes("orders", "1.1.0-beta.1")
	mustContain(t, beta1, "in beta one")
	mustNotContain(t, beta1, "in beta two")

	// ordering: 1.1.0-beta.2 is older than 1.1.0 but newer than 1.1.0-beta.1
	if vs, _ := g.Releases("orders"); len(vs) != 4 || vs[1] != "1.1.0-beta.1" || vs[2] != "1.1.0-beta.2" || vs[3] != "1.1.0" {
		t.Errorf("order: %v", vs)
	}
}

// A lightweight tag (or a tag whose message is not a manifest) for an umbrella is a clear error, not a crash.
func TestUmbrellaWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "t@t")
	run(t, dir, "config", "user.name", "t")
	commit(t, dir, "services/orders/a", "feat: x")
	run(t, dir, "tag", "platform@1.0.0") // lightweight: its "message" is the commit message
	cfg, _ := config.Parse([]byte(cfgYAML))
	g := &Generator{Cfg: cfg, Git: git.New(dir)}
	if _, err := g.Notes("platform", "1.0.0"); err == nil {
		t.Error("expected an error for a tag without a manifest")
	}
}
