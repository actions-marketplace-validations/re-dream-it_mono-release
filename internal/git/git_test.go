package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	tags, err := g.Tags("a", "HEAD")
	if err != nil || len(tags) != 1 || tags[0].Name != "a@1.0.0" || tags[0].Version.String() != "1.0.0" {
		t.Fatalf("Tags: %v %v", tags, err)
	}
	if none, _ := g.Tags("zzz", "HEAD"); len(none) != 0 {
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
		if _, err := g.Tags("a", ref); err != nil {
			t.Fatal(err)
		}
	}
	// tags on main are not reachable from beta; resolve them through main so the reader knows their commits
	if _, err := g.Tags("x", "main"); err != nil {
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
