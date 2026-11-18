// Package git abstracts repository access. The default implementation shells out to the git binary.
package git

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
)

type Commit struct {
	SHA     string
	Author  string
	Subject string
	Body    string
	Files   []string // every path touched (for renames: old and new)
}

type Tag struct {
	Name    string
	Version *semver.Version
	Commit  string // commit the tag points to (peeled); may be empty for non-git readers
}

// strictVersion is the semver.org grammar. Tags whose version part does not match are ignored, so
// "orders-v2-v1.0.0" is never mistaken for a version of "orders-v".
var strictVersion = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// HistoryLoader is implemented by readers that can fetch the history for several tags in one pass.
// Calling it before Commits is optional; Commits falls back to loading on demand.
type HistoryLoader interface {
	// LoadHistory prepares Commits for every base tag ("" means the full history).
	LoadHistory(bases []string) error
}

type Reader interface {
	// Tags returns the version tags named "<prefix><version>" reachable from ref. An empty ref means
	// the reader's current ref (HEAD unless --ref was given); other refs are e.g. "refs/heads/main".
	Tags(prefix, ref string) ([]Tag, error)
	// BranchRefs returns the existing refs for a branch name: the local branch and origin's copy.
	BranchRefs(branch string) []string
	// Commits returns commits in (sinceTag, ref], newest first. Empty sinceTag means full history.
	Commits(sinceTag string) ([]Commit, error)
	// Between returns the commits reachable from to but not from from (empty from: all of them), newest first.
	Between(from, to string) ([]Commit, error)
	// ChangedFiles lists the files changed between the merge base of base and the current ref, and the ref.
	ChangedFiles(base string) ([]string, error)
	// TagInfo returns the commit a tag points to and its date (tagger date, or commit date for lightweight tags).
	TagInfo(tag string) (commit string, date time.Time, err error)
	// Head returns the commit of the current ref.
	Head() (string, error)
	CreateTag(name, sha, message string) error
	// TagMessage returns the message of an annotated tag; ok is false if the tag does not exist.
	TagMessage(tag string) (msg string, ok bool, err error)
	// PushTags pushes all the given tags in one atomic push (one network round trip).
	PushTags(remote string, names []string) error
}

// CLI implements Reader via the git binary. Create it with New.
type CLI struct {
	Dir string
	// Ref is the revision to read ("" = HEAD). Everything - tags, history, the plan - is computed as if it were checked out.
	Ref   string
	trace bool

	mu    sync.Mutex
	stats map[string]*stat

	tagMu     sync.Mutex
	tags      map[string][]Tag  // ref -> every valid-looking version tag (name, parsed later per prefix)
	tagCommit map[string]string // tag name -> commit

	histMu  sync.Mutex
	lists   map[string][]string // base tag ("" = full history) -> commit SHAs, newest first
	commits map[string]Commit   // sha -> commit
}

type stat struct {
	n int
	d time.Duration
}

// TraceEnabled reports whether git call tracing is on (MONO_RELEASE_TRACE=1; SEMVER_TRACE is the old name).
func TraceEnabled() bool {
	return os.Getenv("MONO_RELEASE_TRACE") != "" || os.Getenv("SEMVER_TRACE") != ""
}

// New returns a Reader for the repository in dir. With MONO_RELEASE_TRACE=1 every git call is
// logged to stderr with its duration (see PrintSummary).
func New(dir string) *CLI {
	return &CLI{
		Dir: dir, trace: TraceEnabled(), stats: map[string]*stat{},
		tags: map[string][]Tag{}, tagCommit: map[string]string{}, lists: map[string][]string{}, commits: map[string]Commit{},
	}
}

func (g *CLI) run(args ...string) (string, error) { return g.runIn("", args...) }

// runIn runs git with stdin fed from the given string.
func (g *CLI) runIn(stdin string, args ...string) (string, error) {
	start := time.Now()
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	g.record(args, time.Since(start))
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// subcommand returns the git subcommand name, skipping leading "-c key=value" options.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return "?"
}

func (g *CLI) record(args []string, d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := subcommand(args)
	st := g.stats[k]
	if st == nil {
		st = &stat{}
		g.stats[k] = st
	}
	st.n++
	st.d += d
	if g.trace {
		line := strings.Join(args, " ")
		if len(line) > 140 {
			line = line[:140] + "..."
		}
		fmt.Fprintf(os.Stderr, "[trace] %8s  git %s\n", d.Round(time.Millisecond), line)
	}
}

// PrintSummary writes the number of git calls and total time per subcommand.
func (g *CLI) PrintSummary(w io.Writer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	keys := make([]string, 0, len(g.stats))
	for k := range g.stats {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return g.stats[keys[i]].d > g.stats[keys[j]].d })
	var total time.Duration
	var calls int
	fmt.Fprintln(w, "[trace] git calls summary:")
	for _, k := range keys {
		st := g.stats[k]
		fmt.Fprintf(w, "[trace]   %-12s %5d calls  %10s\n", k, st.n, st.d.Round(time.Millisecond))
		total += st.d
		calls += st.n
	}
	fmt.Fprintf(w, "[trace]   %-12s %5d calls  %10s\n", "TOTAL", calls, total.Round(time.Millisecond))
}

func (g *CLI) ref() string {
	if g.Ref == "" {
		return "HEAD"
	}
	return g.Ref
}

func (g *CLI) Head() (string, error) {
	out, err := g.run("rev-parse", "--verify", g.ref()+"^{commit}")
	return strings.TrimSpace(out), err
}

// Tags returns the tags named "<prefix><version>" reachable from ref (unordered). Every tag of a ref
// is read with a single git call and then served from memory.
func (g *CLI) Tags(prefix, ref string) ([]Tag, error) {
	if ref == "" {
		ref = g.ref()
	}
	g.tagMu.Lock()
	defer g.tagMu.Unlock()
	all, ok := g.tags[ref]
	if !ok {
		// one line per tag: "<name> <object> <peeled object>" (the peeled object is empty for lightweight tags)
		out, err := g.run("for-each-ref", "--merged="+ref,
			"--format=%(refname:strip=2) %(objectname) %(*objectname)", "refs/tags/*")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			name, commit := f[0], f[1]
			if len(f) >= 3 {
				commit = f[2] // annotated tag: use the commit it points to
			}
			g.tagCommit[name] = commit
			all = append(all, Tag{Name: name, Commit: commit})
		}
		g.tags[ref] = all
	}
	var res []Tag
	for _, t := range all {
		ver, ok := strings.CutPrefix(t.Name, prefix)
		if !ok || !strictVersion.MatchString(ver) {
			continue
		}
		v, err := semver.NewVersion(ver)
		if err != nil {
			continue
		}
		t.Version = v
		res = append(res, t)
	}
	return res, nil
}

func (g *CLI) BranchRefs(branch string) []string {
	var refs []string
	for _, ref := range []string{"refs/heads/" + branch, "refs/remotes/origin/" + branch} {
		if _, err := g.run("rev-parse", "--verify", "--quiet", ref); err == nil {
			refs = append(refs, ref)
		}
	}
	return refs
}

// CurrentBranch returns the checked-out branch ("HEAD" when detached).
func (g *CLI) CurrentBranch() (string, error) {
	out, err := g.run("rev-parse", "--abbrev-ref", "HEAD")
	return strings.TrimSpace(out), err
}

// Commits returns commits in (sinceTag, ref], newest first. Empty sinceTag means full history.
func (g *CLI) Commits(sinceTag string) ([]Commit, error) {
	if err := g.LoadHistory([]string{sinceTag}); err != nil {
		return nil, err
	}
	g.histMu.Lock()
	defer g.histMu.Unlock()
	shas := g.lists[sinceTag]
	res := make([]Commit, 0, len(shas))
	for _, sha := range shas {
		res = append(res, g.commits[sha])
	}
	return res, nil
}

// graph reads the commit graph of the current ref once: SHAs newest first and each commit's parents.
func (g *CLI) graph() (order []string, parents map[string][]string, err error) {
	out, err := g.run("rev-list", "--parents", g.ref())
	if err != nil {
		return nil, nil, err
	}
	parents = map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		order = append(order, f[0])
		parents[f[0]] = f[1:]
	}
	return order, parents, nil
}

// ancestors returns the commit and all its ancestors that exist in the parents map.
func ancestors(commit string, parents map[string][]string) map[string]bool {
	seen := map[string]bool{commit: true}
	stack := []string{commit}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, p := range parents[c] {
			if !seen[p] {
				seen[p] = true
				stack = append(stack, p)
			}
		}
	}
	return seen
}

// LoadHistory resolves the commit ranges for all bases at once from a single read of the commit
// graph, then ONE "git log" reads the details of the union of the ranges.
func (g *CLI) LoadHistory(bases []string) error {
	g.histMu.Lock()
	defer g.histMu.Unlock()

	var todo []string
	seen := map[string]bool{}
	for _, b := range bases {
		if _, ok := g.lists[b]; !ok && !seen[b] {
			seen[b] = true
			todo = append(todo, b)
		}
	}
	if len(todo) == 0 {
		return nil
	}

	// 1. ranges. One "rev-list --parents HEAD" gives the whole graph; "everything after tag T"
	// is computed in memory. Only tags outside HEAD's history need their own rev-list.
	order, parents, err := g.graph()
	if err != nil {
		return err
	}
	reachCache := map[string]map[string]bool{} // tag commit -> its ancestors within HEAD's history
	type res struct {
		shas []string
		err  error
	}
	results := make([]res, len(todo))
	var slow []int // indexes needing a separate git call
	for i, b := range todo {
		if b == "" {
			results[i] = res{shas: order}
			continue
		}
		commit, ok := g.tagCommit[b]
		if !ok {
			slow = append(slow, i)
			continue
		}
		if _, inHistory := parents[commit]; !inHistory {
			slow = append(slow, i) // not an ancestor of the ref (e.g. stable tag on another branch)
			continue
		}
		reach, ok := reachCache[commit]
		if !ok {
			reach = ancestors(commit, parents)
			reachCache[commit] = reach
		}
		rng := make([]string, 0, len(order))
		for _, sha := range order {
			if !reach[sha] {
				rng = append(rng, sha)
			}
		}
		results[i] = res{shas: rng}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(2, min(8, runtime.NumCPU())))
	for _, i := range slow {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, err := g.run("rev-list", todo[i]+"..HEAD")
			results[i] = res{strings.Fields(out), err}
		}(i)
	}
	wg.Wait()

	var need []string
	needSet := map[string]bool{}
	for i, r := range results {
		if r.err != nil {
			return r.err
		}
		g.lists[todo[i]] = r.shas
		for _, sha := range r.shas {
			if _, ok := g.commits[sha]; !ok && !needSet[sha] {
				needSet[sha] = true
				need = append(need, sha)
			}
		}
	}
	if len(need) == 0 {
		return nil
	}

	// 2. details of every needed commit in a single call
	return g.loadDetails(need)
}

// loadDetails reads subject, body and files of the given commits with ONE git call into the cache.
// The caller must hold histMu.
func (g *CLI) loadDetails(shas []string) error {
	if len(shas) == 0 {
		return nil
	}
	out, err := g.runIn(strings.Join(shas, "\n")+"\n",
		"-c", "core.quotepath=false", "log", "--no-walk=unsorted", "--stdin",
		"--diff-merges=first-parent", "--no-renames", "--no-ext-diff", "--no-color",
		"--name-only", "--format=%x1e%H%x1f%an%x1f%s%x1f%b%x1f")
	if err != nil {
		return err
	}
	for _, rec := range strings.Split(out, "\x1e") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", 5)
		if len(f) < 5 {
			continue
		}
		c := Commit{SHA: f[0], Author: f[1], Subject: f[2], Body: f[3]}
		for _, line := range strings.Split(f[4], "\n") {
			if line = strings.TrimRight(line, "\r"); line != "" {
				c.Files = append(c.Files, line)
			}
		}
		g.commits[c.SHA] = c
	}
	return nil
}

// Between returns the commits reachable from "to" but not from "from", newest first.
func (g *CLI) Between(from, to string) ([]Commit, error) {
	rng := to
	if from != "" {
		rng = from + ".." + to
	}
	out, err := g.run("rev-list", rng)
	if err != nil {
		return nil, err
	}
	shas := strings.Fields(out)
	g.histMu.Lock()
	defer g.histMu.Unlock()
	var need []string
	for _, sha := range shas {
		if _, ok := g.commits[sha]; !ok {
			need = append(need, sha)
		}
	}
	if err := g.loadDetails(need); err != nil {
		return nil, err
	}
	res := make([]Commit, 0, len(shas))
	for _, sha := range shas {
		res = append(res, g.commits[sha])
	}
	return res, nil
}

// ChangedFiles lists files changed on the current ref since it diverged from base (three-dot diff).
func (g *CLI) ChangedFiles(base string) ([]string, error) {
	out, err := g.run("-c", "core.quotepath=false", "diff", "--name-only", "--no-renames", base+"..."+g.ref())
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// TagInfo returns the commit a tag points to and the tag's date.
func (g *CLI) TagInfo(tag string) (string, time.Time, error) {
	out, err := g.run("for-each-ref", "--format=%(objectname) %(*objectname) %(creatordate:iso-strict)", "refs/tags/"+tag)
	if err != nil {
		return "", time.Time{}, err
	}
	f := strings.Fields(out)
	if len(f) < 2 {
		return "", time.Time{}, fmt.Errorf("tag %s not found", tag)
	}
	commit, dateStr := f[0], f[len(f)-1]
	if len(f) >= 3 {
		commit = f[1] // annotated: the peeled object is the commit
	}
	t, _ := time.Parse(time.RFC3339, dateStr)
	return commit, t, nil
}

// RemoteURL returns the URL of a remote, or "" if there is none.
func (g *CLI) RemoteURL(remote string) string {
	out, err := g.run("remote", "get-url", remote)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (g *CLI) CreateTag(name, sha, message string) error {
	if message == "" {
		message = name
	}
	_, err := g.run("tag", "-a", name, sha, "-m", message)
	// the tag lists read so far are stale now
	g.tagMu.Lock()
	g.tags = map[string][]Tag{}
	g.tagMu.Unlock()
	return err
}

func (g *CLI) TagMessage(tag string) (string, bool, error) {
	if _, err := g.run("rev-parse", "--verify", "--quiet", "refs/tags/"+tag); err != nil {
		return "", false, nil
	}
	out, err := g.run("for-each-ref", "--format=%(contents)", "refs/tags/"+tag)
	if err != nil {
		return "", false, err
	}
	return strings.TrimRight(out, "\n"), true, nil
}

func (g *CLI) PushTags(remote string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	args := []string{"push", "--atomic", remote}
	for _, n := range names {
		args = append(args, "refs/tags/"+n)
	}
	_, err := g.run(args...)
	return err
}
