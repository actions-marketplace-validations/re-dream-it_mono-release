// Package notes generates release notes in Markdown from tags and commits.
//
// The notes of a release cover everything between the previous release of the same component and
// the release itself, so they can be produced for any existing tag, not only for the one just created.
// An umbrella's notes list the versions of its members and the changes of every member that changed
// since the previous umbrella release.
package notes

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/re-dream-it/mono-release/internal/commits"
	"github.com/re-dream-it/mono-release/internal/config"
	"github.com/re-dream-it/mono-release/internal/git"
	"github.com/re-dream-it/mono-release/internal/planner"
)

// Generator produces notes for the components and umbrellas of a configuration.
type Generator struct {
	Cfg *config.Config
	Git git.Reader
	// RepoURL is the web URL of the repository ("https://github.com/org/repo"); commit and compare
	// links are only produced when it is set.
	RepoURL string
	// Style is "github" or "gitlab" and decides the shape of commit and compare links
	// ("/commit/" vs "/-/commit/"). Empty: guessed from the host name.
	Style string
}

// release is one existing tag of a component or umbrella.
type release struct {
	Version *semver.Version
	Tag     string
}

// releases returns every version tag of name reachable from the current ref, oldest first.
func (g *Generator) releases(name string) ([]release, error) {
	tags, err := g.Git.Tags(g.Cfg.TagPrefix(name), "")
	if err != nil {
		return nil, err
	}
	rels := make([]release, 0, len(tags))
	for _, t := range tags {
		rels = append(rels, release{t.Version, t.Name})
	}
	sort.Slice(rels, func(i, j int) bool { return rels[i].Version.LessThan(rels[j].Version) })
	return rels, nil
}

// previous finds the release before rels[i]: for a stable version the previous stable one,
// for a pre-release simply the previous version.
func previous(rels []release, i int) (release, bool) {
	stable := rels[i].Version.Prerelease() == ""
	for j := i - 1; j >= 0; j-- {
		if stable && rels[j].Version.Prerelease() != "" {
			continue
		}
		return rels[j], true
	}
	return release{}, false
}

func (g *Generator) isUmbrella(name string) bool {
	_, ok := g.Cfg.Umbrellas[name]
	return ok
}

// Releases returns the versions that have notes for name, oldest first.
func (g *Generator) Releases(name string) ([]string, error) {
	rels, err := g.releases(name)
	if err != nil {
		return nil, err
	}
	var vs []string
	for _, r := range rels {
		vs = append(vs, r.Version.String())
	}
	return vs, nil
}

// Notes returns the Markdown notes of one release. An empty version means the latest one.
func (g *Generator) Notes(name, version string) (string, error) {
	if _, ok := g.Cfg.Components[name]; !ok && !g.isUmbrella(name) {
		return "", fmt.Errorf("unknown component or umbrella %q", name)
	}
	rels, err := g.releases(name)
	if err != nil {
		return "", err
	}
	if len(rels) == 0 {
		return "", fmt.Errorf("%s has no releases yet", name)
	}
	i := len(rels) - 1
	if version != "" {
		i = -1
		for k, r := range rels {
			if r.Version.String() == version {
				i = k
			}
		}
		if i < 0 {
			return "", fmt.Errorf("%s has no release %s", name, version)
		}
	}
	return g.section(name, rels, i, "##")
}

// Changelog returns the notes of every release, newest first.
func (g *Generator) Changelog(name string) (string, error) {
	if _, ok := g.Cfg.Components[name]; !ok && !g.isUmbrella(name) {
		return "", fmt.Errorf("unknown component or umbrella %q", name)
	}
	rels, err := g.releases(name)
	if err != nil {
		return "", err
	}
	if len(rels) == 0 {
		return "", fmt.Errorf("%s has no releases yet", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s changelog\n\n", name)
	for i := len(rels) - 1; i >= 0; i-- {
		s, err := g.section(name, rels, i, "##")
		if err != nil {
			return "", err
		}
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String(), nil
}

// section renders one release; level is the heading marker of its title.
func (g *Generator) section(name string, rels []release, i int, level string) (string, error) {
	cur := rels[i]
	commit, date, err := g.Git.TagInfo(cur.Tag)
	if err != nil {
		return "", err
	}
	prev, hasPrev := previous(rels, i)

	var b strings.Builder
	title := fmt.Sprintf("%s %s %s", level, name, cur.Version)
	if !date.IsZero() {
		title += " (" + date.UTC().Format("2006-01-02") + ")"
	}
	b.WriteString(title + "\n\n")

	if g.isUmbrella(name) {
		if err := g.umbrellaBody(&b, name, cur, prev, hasPrev, level); err != nil {
			return "", err
		}
	} else {
		from := ""
		if hasPrev {
			from, _, err = g.Git.TagInfo(prev.Tag)
			if err != nil {
				return "", err
			}
		}
		cs, err := g.Git.Between(from, commit)
		if err != nil {
			return "", err
		}
		body := g.groups(name, cs)
		if body == "" {
			body = "_No changes of its own: released because a dependency changed._\n"
		}
		b.WriteString(body)
	}
	if hasPrev {
		if link := g.compareLink(prev.Tag, cur.Tag); link != "" {
			fmt.Fprintf(&b, "\n[Full diff](%s)\n", link)
		}
	}
	return b.String(), nil
}

// umbrellaBody writes the member table and the notes of every member that changed.
func (g *Generator) umbrellaBody(b *strings.Builder, name string, cur, prev release, hasPrev bool, level string) error {
	msg, ok, err := g.Git.TagMessage(cur.Tag)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tag %s not found", cur.Tag)
	}
	members, err := planner.ParseManifest(msg)
	if err != nil {
		return fmt.Errorf("tag %s: %w", cur.Tag, err)
	}
	var before map[string]string
	if hasPrev {
		if pmsg, ok, _ := g.Git.TagMessage(prev.Tag); ok {
			before, _ = planner.ParseManifest(pmsg)
		}
	}

	names := make([]string, 0, len(members))
	for n := range members {
		names = append(names, n)
	}
	sort.Strings(names)

	b.WriteString("| Component | Version |\n|---|---|\n")
	changed := map[string]bool{}
	for _, n := range names {
		v, old := members[n], before[n]
		switch {
		case before == nil || old == v:
			fmt.Fprintf(b, "| %s | %s |\n", n, v)
		case old == "":
			fmt.Fprintf(b, "| %s | %s (new) |\n", n, v)
			changed[n] = true
		default:
			fmt.Fprintf(b, "| %s | %s → %s |\n", n, old, v)
			changed[n] = true
		}
	}
	b.WriteString("\n")

	sub := level + "#"
	for _, n := range names {
		if !changed[n] {
			continue
		}
		v, old := members[n], before[n]
		heading := fmt.Sprintf("%s %s %s", sub, n, v)
		if old != "" {
			heading = fmt.Sprintf("%s %s %s → %s", sub, n, old, v)
		}
		b.WriteString(heading + "\n\n")
		if old == "" {
			b.WriteString("_New in this release._\n\n")
			continue
		}
		to, _, err := g.Git.TagInfo(g.Cfg.TagName(n, v))
		if err != nil {
			b.WriteString("_Tag of this version was not found._\n\n")
			continue
		}
		from, _, err := g.Git.TagInfo(g.Cfg.TagName(n, old))
		if err != nil {
			b.WriteString("_Tag of the previous version was not found._\n\n")
			continue
		}
		cs, err := g.Git.Between(from, to)
		if err != nil {
			return err
		}
		body := g.groups(n, cs)
		if body == "" {
			body = "_No changes of its own: released because a dependency changed._\n"
		}
		b.WriteString(body + "\n")
	}
	return nil
}

// entry is a commit prepared for display.
type entry struct {
	line     string
	typ      string
	breaking bool
}

// groups turns the commits relevant to a component into grouped Markdown lists.
func (g *Generator) groups(name string, cs []git.Commit) string {
	var items []entry
	for _, c := range cs {
		if commits.IsMerge(c.Subject) {
			continue
		}
		p := commits.Parse(c.Subject, c.Body)
		if p.Skip || planner.Relevance(g.Cfg, name, c, p) == "" {
			continue
		}
		subject := c.Subject
		if p.Type != "" {
			subject = p.Subject
			if p.Scope != "" {
				subject = "**" + p.Scope + ":** " + subject
			}
		}
		items = append(items, entry{line: "- " + subject + g.commitLink(c.SHA), typ: p.Type, breaking: p.Breaking})
	}
	if len(items) == 0 {
		return ""
	}

	var b strings.Builder
	section := func(title string, pick func(entry) bool) {
		var lines []string
		for _, it := range items {
			if pick(it) {
				lines = append(lines, it.line)
			}
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "**%s**\n\n%s\n\n", title, strings.Join(lines, "\n"))
		}
	}
	section("Breaking changes", func(e entry) bool { return e.breaking })
	section("Features", func(e entry) bool { return !e.breaking && e.typ == "feat" })
	section("Bug fixes", func(e entry) bool { return !e.breaking && e.typ == "fix" })
	section("Performance", func(e entry) bool { return !e.breaking && e.typ == "perf" })

	var other []string
	for _, it := range items {
		if !it.breaking && it.typ != "feat" && it.typ != "fix" && it.typ != "perf" {
			other = append(other, it.line)
		}
	}
	if len(other) > 0 {
		fmt.Fprintf(&b, "<details><summary>Other changes (%d)</summary>\n\n%s\n\n</details>\n", len(other), strings.Join(other, "\n"))
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func (g *Generator) commitLink(sha string) string {
	short := sha
	if len(short) > 7 {
		short = short[:7]
	}
	if g.RepoURL == "" {
		return " (" + short + ")"
	}
	return fmt.Sprintf(" ([%s](%s%s%s))", short, g.RepoURL, g.commitPath(), sha)
}

func (g *Generator) compareLink(from, to string) string {
	if g.RepoURL == "" {
		return ""
	}
	return fmt.Sprintf("%s%s%s...%s", g.RepoURL, g.comparePath(), url.PathEscape(from), url.PathEscape(to))
}

// gitlabStyle reports whether links should use GitLab's "/-/" routes.
func (g *Generator) gitlabStyle() bool {
	switch g.Style {
	case "gitlab":
		return true
	case "github":
		return false
	}
	u, err := url.Parse(g.RepoURL)
	return err == nil && strings.Contains(u.Host, "gitlab")
}

func (g *Generator) commitPath() string {
	if g.gitlabStyle() {
		return "/-/commit/"
	}
	return "/commit/"
}

func (g *Generator) comparePath() string {
	if g.gitlabStyle() {
		return "/-/compare/"
	}
	return "/compare/"
}

// WebURL turns a git remote URL into the repository's web URL, dropping credentials:
// "https://oauth2:TOKEN@gitlab.com/g/p.git" and "git@github.com:o/r.git" become
// "https://gitlab.com/g/p" and "https://github.com/o/r". It returns "" for anything it does not understand.
func WebURL(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	var host, path string
	switch {
	case strings.Contains(remote, "://"):
		u, err := url.Parse(remote)
		if err != nil || u.Host == "" {
			return ""
		}
		host, path = u.Hostname(), u.Path // userinfo (tokens!) is dropped on purpose
		if u.Scheme == "http" {
			return "http://" + u.Host + strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
		}
	case strings.Contains(remote, "@") && strings.Contains(remote, ":"): // scp-like: git@host:org/repo.git
		afterAt := remote[strings.Index(remote, "@")+1:]
		h, p, ok := strings.Cut(afterAt, ":")
		if !ok {
			return ""
		}
		host, path = h, "/"+p
	default:
		return ""
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	if host == "" || path == "" || path == "/" {
		return ""
	}
	return "https://" + host + path
}

// Write generates the files configured in the "notes" section of the configuration, relative to
// dir, and returns their paths. Only entries for the given name are written when name is not empty.
func (g *Generator) Write(dir, name string) ([]string, error) {
	var written []string
	for _, e := range g.Cfg.Notes {
		var targets []string
		if e.For == "*" {
			targets = append(targets, g.Cfg.Names()...)
			targets = append(targets, g.Cfg.UmbrellaNames()...)
		} else {
			targets = []string{e.For}
		}
		for _, t := range targets {
			if name != "" && t != name {
				continue
			}
			versions, err := g.Releases(t)
			if err != nil {
				return written, err
			}
			if len(versions) == 0 {
				continue // nothing released yet, nothing to write
			}
			var content string
			file := strings.ReplaceAll(e.File, "{name}", t)
			if e.History == "all" {
				content, err = g.Changelog(t)
			} else {
				latest := versions[len(versions)-1]
				file = strings.ReplaceAll(file, "{version}", latest)
				content, err = g.Notes(t, latest)
			}
			if err != nil {
				return written, fmt.Errorf("notes for %s: %w", t, err)
			}
			full := filepath.Join(dir, filepath.FromSlash(file))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return written, err
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				return written, err
			}
			written = append(written, file)
		}
	}
	return written, nil
}
