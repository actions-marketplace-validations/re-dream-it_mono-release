package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func sh(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCLI(t *testing.T) {
	d := t.TempDir()
	sh(t, d, "init", "-q")
	sh(t, d, "config", "user.email", "t@t")
	sh(t, d, "config", "user.name", "t")
	write(t, d, "svc a/f", "1")
	sh(t, d, "add", "-A")
	sh(t, d, "commit", "-qm", "feat: init")
	g := New(d)
	head, _ := g.Head()
	if err := g.CreateTag("a@1.0.0", head, ""); err != nil {
		t.Fatal(err)
	}
	tags, err := g.Tags("a@", "HEAD")
	if err != nil || len(tags) != 1 || tags[0].Name != "a@1.0.0" || tags[0].Version.String() != "1.0.0" {
		t.Fatalf("Tags: %v %v", tags, err)
	}
	if none, _ := g.Tags("zzz@", "HEAD"); len(none) != 0 {
		t.Error("unexpected tags for unknown component")
	}
	br0, _ := g.CurrentBranch()
	if refs := g.BranchRefs(br0); len(refs) != 1 || refs[0] != "refs/heads/"+br0 {
		t.Errorf("BranchRefs: %v", refs)
	}
	if refs := g.BranchRefs("no-such-branch"); len(refs) != 0 {
		t.Errorf("BranchRefs for missing branch: %v", refs)
	}
	if br, err := g.CurrentBranch(); err != nil || br == "" {
		t.Errorf("CurrentBranch: %q %v", br, err)
	}

	write(t, d, "docs/x.md", "hello")
	sh(t, d, "mv", "svc a/f", "svc a/g")
	sh(t, d, "add", "-A")
	sh(t, d, "commit", "-qm", "fix: stuff", "-m", "body text")

	cs, err := g.Commits("a@1.0.0")
	if err != nil || len(cs) != 1 {
		t.Fatalf("Commits: %v %v", cs, err)
	}
	c := cs[0]
	if c.Subject != "fix: stuff" || strings.TrimSpace(c.Body) != "body text" {
		t.Errorf("subject/body: %q %q", c.Subject, c.Body)
	}
	wantFiles := map[string]bool{"docs/x.md": true, "svc a/f": true, "svc a/g": true}
	for _, f := range c.Files {
		delete(wantFiles, f)
	}
	if len(wantFiles) != 0 {
		t.Errorf("Files missing %v, got %v", wantFiles, c.Files)
	}
	if all, _ := g.Commits(""); len(all) != 2 {
		t.Errorf("full history: %d", len(all))
	}
}

func TestMergeCommitFiles(t *testing.T) {
	d := t.TempDir()
	sh(t, d, "init", "-q", "-b", "main")
	sh(t, d, "config", "user.email", "t@t")
	sh(t, d, "config", "user.name", "t")
	write(t, d, "base", "1")
	sh(t, d, "add", "-A")
	sh(t, d, "commit", "-qm", "init")
	sh(t, d, "tag", "base")
	sh(t, d, "checkout", "-qb", "mr")
	write(t, d, "b/x", "1")
	sh(t, d, "add", "-A")
	sh(t, d, "commit", "-qm", "wip")
	sh(t, d, "checkout", "-q", "main")
	write(t, d, "a/y", "1") // main moves on independently
	sh(t, d, "add", "-A")
	sh(t, d, "commit", "-qm", "docs: main change")
	sh(t, d, "merge", "-q", "--no-ff", "mr", "-m", "feat: b improvements")

	cs, err := New(d).Commits("base")
	if err != nil {
		t.Fatal(err)
	}
	var merge *Commit
	for i := range cs {
		if cs[i].Subject == "feat: b improvements" {
			merge = &cs[i]
		}
	}
	if merge == nil {
		t.Fatalf("merge commit missing: %+v", cs)
	}
	// the merge shows what it brought into main (b/x), not what main already had (a/y)
	if len(merge.Files) != 1 || merge.Files[0] != "b/x" {
		t.Errorf("merge files: %v", merge.Files)
	}
}

// Commits(base) must equal a direct "git rev-list base..HEAD" for every kind of base:
// annotated and lightweight tags, a merge in history, and a tag that is NOT an ancestor of HEAD.
func TestCommitsMatchRevList(t *testing.T) {
	d := t.TempDir()
	sh(t, d, "init", "-q", "-b", "main")
	sh(t, d, "config", "user.email", "t@t")
	sh(t, d, "config", "user.name", "t")
	commit := func(file, msg string) {
		write(t, d, file, msg)
		sh(t, d, "add", "-A")
		sh(t, d, "commit", "-qm", msg)
	}
	commit("a/1", "feat: one")
	sh(t, d, "tag", "-a", "a@1.0.0", "-m", "annotated")
	commit("a/2", "fix: two")
	sh(t, d, "checkout", "-qb", "feature")
	commit("b/1", "feat: on feature")
	commit("b/2", "fix: on feature 2")
	sh(t, d, "checkout", "-q", "main")
	commit("a/3", "feat: three")
	sh(t, d, "tag", "b@2.0.0") // lightweight
	sh(t, d, "merge", "-q", "--no-ff", "feature", "-m", "Merge branch 'feature'")
	commit("a/4", "fix: four")
	// a stable tag on main that a "beta" branch (cut from an older commit) never merged
	sh(t, d, "checkout", "-qb", "beta", "a@1.0.0")
	commit("a/beta", "feat: beta only")
	sh(t, d, "tag", "-a", "other@1.0.0", "-m", "x", "main")

	g := New(d)
	for _, ref := range []string{"HEAD"} {
		if _, err := g.Tags("a@", ref); err != nil {
			t.Fatal(err)
		}
	}
	// tags on main are not reachable from beta; resolve them through main so the reader knows their commits
	if _, err := g.Tags("x@", "main"); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) []string {
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.Fields(string(out))
	}

	for _, base := range []string{"", "a@1.0.0", "b@2.0.0", "other@1.0.0"} {
		want := run("rev-list", map[bool]string{true: "HEAD", false: base + "..HEAD"}[base == ""])
		got, err := g.Commits(base)
		if err != nil {
			t.Fatalf("%q: %v", base, err)
		}
		var gotSHAs []string
		for _, c := range got {
			gotSHAs = append(gotSHAs, c.SHA)
			if c.Subject == "" {
				t.Errorf("%q: commit %s has no details", base, c.SHA)
			}
		}
		if !reflect.DeepEqual(gotSHAs, want) {
			t.Errorf("base %q:\n got %v\nwant %v", base, gotSHAs, want)
		}
	}
	// one batched history load must give the same answers as loading on demand
	g2 := New(d)
	if err := g2.LoadHistory([]string{"", "a@1.0.0", "other@1.0.0"}); err != nil {
		t.Fatal(err)
	}
	a, _ := g.Commits("a@1.0.0")
	b, _ := g2.Commits("a@1.0.0")
	if !reflect.DeepEqual(a, b) {
		t.Error("batched and on-demand history differ")
	}
}

func TestPushTags(t *testing.T) {
	remote := t.TempDir()
	sh(t, remote, "init", "-q", "--bare")
	d := t.TempDir()
	sh(t, d, "init", "-q", "-b", "main")
	sh(t, d, "config", "user.email", "t@t")
	sh(t, d, "config", "user.name", "t")
	write(t, d, "f", "1")
	sh(t, d, "add", "-A")
	sh(t, d, "commit", "-qm", "init")
	sh(t, d, "remote", "add", "origin", remote)
	g := New(d)
	head, _ := g.Head()
	for _, n := range []string{"a@1.0.0", "b@1.0.0", "platform@1.0.0"} {
		if err := g.CreateTag(n, head, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.PushTags("origin", []string{"a@1.0.0", "b@1.0.0", "platform@1.0.0"}); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", remote, "tag", "-l").Output()
	if err != nil || len(strings.Fields(string(out))) != 3 {
		t.Errorf("remote tags: %q %v", out, err)
	}
	if err := g.PushTags("origin", nil); err != nil {
		t.Error("empty push should be a no-op:", err)
	}
}

func TestTagMessage(t *testing.T) {
	d := t.TempDir()
	sh(t, d, "init", "-q", "-b", "main")
	sh(t, d, "config", "user.email", "t@t")
	sh(t, d, "config", "user.name", "t")
	write(t, d, "f", "1")
	sh(t, d, "add", "-A")
	sh(t, d, "commit", "-qm", "init")
	g := New(d)
	head, _ := g.Head()
	if err := g.CreateTag("platform@1.0.0", head, "a@1.0.0\nb@2.0.0\n"); err != nil {
		t.Fatal(err)
	}
	msg, ok, err := g.TagMessage("platform@1.0.0")
	if err != nil || !ok || msg != "a@1.0.0\nb@2.0.0" {
		t.Errorf("TagMessage: %q %v %v", msg, ok, err)
	}
	if _, ok, err := g.TagMessage("platform@9.9.9"); ok || err != nil {
		t.Errorf("missing tag: %v %v", ok, err)
	}
}

func newRepo(t *testing.T) (string, *CLI) {
	t.Helper()
	d := t.TempDir()
	sh(t, d, "init", "-q", "-b", "main")
	sh(t, d, "config", "user.email", "t@t")
	sh(t, d, "config", "user.name", "t")
	return d, New(d)
}

func commitFile(t *testing.T, d, file, msg string) {
	t.Helper()
	write(t, d, file, msg)
	sh(t, d, "add", "-A")
	sh(t, d, "commit", "-qm", msg)
}

func TestTagsPrefixAndStrictVersions(t *testing.T) {
	d, g := newRepo(t)
	commitFile(t, d, "f", "init")
	for _, n := range []string{
		"orders@1.0.0", "orders@1.2.0-beta.1", // versions of "orders"
		"orders-api@3.0.0", // another component whose name starts like "orders"
		"orders@1.2",       // not a full version
		"orders@01.0.0",    // leading zero
		"orders@v1.0.0",    // a v prefix is not part of the format
		"v9.9.9", "release-1",
	} {
		sh(t, d, "tag", n)
	}
	got, err := g.Tags("orders@", "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tg := range got {
		names = append(names, tg.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"orders@1.0.0", "orders@1.2.0-beta.1"}) {
		t.Errorf("orders tags: %v", names)
	}
	if got, _ := g.Tags("orders-api@", ""); len(got) != 1 || got[0].Version.String() != "3.0.0" {
		t.Errorf("orders-api tags: %v", got)
	}
}

func TestRefReadsAnotherBranch(t *testing.T) {
	d, g := newRepo(t)
	commitFile(t, d, "a/1", "feat: one")
	sh(t, d, "tag", "-a", "a@1.0.0", "-m", "x")
	sh(t, d, "checkout", "-qb", "other")
	commitFile(t, d, "a/2", "fix: on other")
	sh(t, d, "tag", "-a", "a@1.0.1", "-m", "x")
	sh(t, d, "checkout", "-q", "main") // main does not have a@1.0.1

	if tags, _ := g.Tags("a@", ""); len(tags) != 1 {
		t.Errorf("HEAD (main) tags: %v", tags)
	}
	r := New(d)
	r.Ref = "other"
	tags, err := r.Tags("a@", "")
	if err != nil || len(tags) != 2 {
		t.Fatalf("--ref other: %v %v", tags, err)
	}
	head, _ := r.Head()
	want := strings.TrimSpace(func() string { o, _ := exec.Command("git", "-C", d, "rev-parse", "other").Output(); return string(o) }())
	if head != want {
		t.Errorf("Head with ref: %s want %s", head, want)
	}
	cs, err := r.Commits("a@1.0.0")
	if err != nil || len(cs) != 1 || cs[0].Subject != "fix: on other" {
		t.Errorf("Commits with ref: %v %v", cs, err)
	}
}

func TestBetweenChangedFilesTagInfo(t *testing.T) {
	d, g := newRepo(t)
	commitFile(t, d, "a/1", "feat: one")
	sh(t, d, "tag", "-a", "a@1.0.0", "-m", "annotated")
	commitFile(t, d, "a/2", "fix: two")
	commitFile(t, d, "b/1", "feat: three")
	sh(t, d, "tag", "b@1.0.0") // lightweight
	sh(t, d, "checkout", "-qb", "topic", "a@1.0.0")
	commitFile(t, d, "c/1", "feat: on topic")
	sh(t, d, "checkout", "-q", "main")

	cs, err := g.Between("a@1.0.0", "b@1.0.0")
	if err != nil || len(cs) != 2 || cs[0].Subject != "feat: three" || cs[1].Subject != "fix: two" {
		t.Errorf("Between: %v %v", cs, err)
	}
	if all, _ := g.Between("", "a@1.0.0"); len(all) != 1 {
		t.Errorf("Between from root: %d", len(all))
	}
	files, err := g.ChangedFiles("topic")
	sort.Strings(files)
	if err != nil || !reflect.DeepEqual(files, []string{"a/2", "b/1"}) {
		t.Errorf("ChangedFiles(topic) = %v %v (main changed a/2, b/1 since topic diverged)", files, err)
	}
	commit, date, err := g.TagInfo("a@1.0.0")
	if err != nil || len(commit) != 40 || date.IsZero() {
		t.Errorf("TagInfo annotated: %q %v %v", commit, date, err)
	}
	head, _ := g.Head()
	if c, d2, err := g.TagInfo("b@1.0.0"); err != nil || c != head || d2.IsZero() {
		t.Errorf("TagInfo lightweight: %q %v %v", c, d2, err)
	}
	if _, _, err := g.TagInfo("nope@1.0.0"); err == nil {
		t.Error("expected error for missing tag")
	}
}

func TestCreateTagRefreshesCachedTags(t *testing.T) {
	d, g := newRepo(t)
	commitFile(t, d, "f", "init")
	head, _ := g.Head()
	if err := g.CreateTag("a@1.0.0", head, ""); err != nil {
		t.Fatal(err)
	}
	if tags, _ := g.Tags("a@", ""); len(tags) != 1 { // fills the cache
		t.Fatalf("tags: %v", tags)
	}
	if err := g.CreateTag("a@1.1.0", head, ""); err != nil {
		t.Fatal(err)
	}
	if tags, _ := g.Tags("a@", ""); len(tags) != 2 {
		t.Errorf("a tag created after the first read must be visible: %v", tags)
	}
}
