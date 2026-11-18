package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/re-dream-it/mono-release/internal/config"
	"github.com/re-dream-it/mono-release/internal/git"
	"github.com/re-dream-it/mono-release/internal/notes"
	"github.com/re-dream-it/mono-release/internal/planner"
)

// cmdVersion prints the latest released version(s). A single name prints just the version,
// so it can be used in scripts: VERSION=$(monorelease version orders).
func cmdVersion(cfg *config.Config, g git.Reader, opt planner.Options, o options) error {
	if len(o.args) > 1 {
		return fmt.Errorf("version takes at most one name")
	}
	v, err := planner.Latest(cfg, g, opt)
	if err != nil {
		return err
	}

	if len(o.args) == 1 {
		name := o.args[0]
		var ver string
		var ok bool
		if _, isComp := cfg.Components[name]; isComp {
			ver, ok = v.Components[name]
		} else if _, isUmb := cfg.Umbrellas[name]; isUmb {
			ver, ok = v.Umbrellas[name]
		} else {
			return fmt.Errorf("unknown component or umbrella %q", name)
		}
		if !ok {
			return fmt.Errorf("%s has no releases yet", name)
		}
		if o.asJSON {
			return printJSON(map[string]string{"name": name, "version": ver})
		}
		fmt.Println(ver)
		return nil
	}

	if o.asJSON {
		return printJSON(v)
	}
	type row struct{ label, ver string }
	var rows []row
	width := 0
	add := func(prefix, name string, vs map[string]string) {
		ver, ok := vs[name]
		if !ok {
			ver = "(none)"
		}
		rows = append(rows, row{prefix + name, ver})
		width = max(width, len(prefix+name))
	}
	for _, n := range cfg.Names() {
		add("", n, v.Components)
	}
	for _, n := range cfg.UmbrellaNames() {
		add("umbrella ", n, v.Umbrellas)
	}
	for _, r := range rows {
		fmt.Printf("%-*s  %s\n", width, r.label, r.ver)
	}
	return nil
}

// cmdManifest prints the member versions recorded in an umbrella release tag.
func cmdManifest(cfg *config.Config, g git.Reader, opt planner.Options, o options) error {
	if len(o.args) < 1 || len(o.args) > 2 {
		return fmt.Errorf("usage: monorelease manifest <umbrella> [version]")
	}
	name := o.args[0]
	if _, ok := cfg.Umbrellas[name]; !ok {
		return fmt.Errorf("unknown umbrella %q", name)
	}
	var ver string
	if len(o.args) == 2 {
		ver = strings.TrimPrefix(o.args[1], cfg.TagPrefix(name))
	} else {
		v, err := planner.Latest(cfg, g, opt)
		if err != nil {
			return err
		}
		var ok bool
		if ver, ok = v.Umbrellas[name]; !ok {
			return fmt.Errorf("umbrella %s has no releases yet", name)
		}
	}
	tag := cfg.TagName(name, ver)
	msg, ok, err := g.TagMessage(tag)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tag %s not found", tag)
	}
	members, err := planner.ParseManifest(msg)
	if err != nil {
		return fmt.Errorf("tag %s: %w (not created by monorelease?)", tag, err)
	}
	if o.component != "" {
		if _, ok := cfg.Components[o.component]; !ok {
			return fmt.Errorf("unknown component %q", o.component)
		}
		cv, ok := members[o.component]
		if !ok {
			return fmt.Errorf("component %s is not part of %s", o.component, tag)
		}
		if o.asJSON {
			return printJSON(map[string]string{"umbrella": name, "umbrella_version": ver, "component": o.component, "version": cv})
		}
		fmt.Println(cv) // just the version, handy in scripts
		return nil
	}
	if o.asJSON {
		return printJSON(map[string]any{"name": name, "version": ver, "tag": tag, "members": members})
	}
	fmt.Print(planner.UmbrellaRelease{Members: members}.Manifest())
	return nil
}

// cmdChanged lists the components with changes, one per line.
func cmdChanged(cfg *config.Config, g git.Reader, opt planner.Options, o options) error {
	names, err := planner.Changed(cfg, g, planner.ChangedOptions{Options: opt, Base: o.base, WithDependents: o.withDependents})
	if err != nil {
		return err
	}
	if o.asJSON {
		if names == nil {
			names = []string{}
		}
		return printJSON(names)
	}
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

// cmdExplain says why a component or umbrella is, or is not, released.
func cmdExplain(cfg *config.Config, g git.Reader, opt planner.Options, o options) error {
	if len(o.args) != 1 {
		return fmt.Errorf("usage: monorelease explain <component|umbrella>")
	}
	name := o.args[0]
	opt.Explain = true
	plan, err := planner.Build(cfg, g, opt)
	if err != nil {
		return err
	}
	d, ok := plan.Details[name]
	if !ok {
		return fmt.Errorf("unknown component or umbrella %q", name)
	}
	if o.asJSON {
		return printJSON(d)
	}
	printExplain(d)
	return nil
}

func printExplain(d *planner.Detail) {
	fmt.Printf("%s (%s)\n", d.Name, d.Kind)
	ch := "stable"
	if d.Channel != "" {
		ch = "pre-release channel " + d.Channel
	}
	fmt.Printf("  branch:   %s\n", ch)
	if d.Current != "" {
		fmt.Printf("  current:  %s (tag %s)\n", d.Current, d.CurrentTag)
	} else {
		fmt.Println("  current:  never released")
	}

	if d.Kind == "umbrella" {
		names := make([]string, 0, len(d.Members))
		for n := range d.Members {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Println("  members:")
		for _, n := range names {
			fmt.Printf("    %-20s %s\n", n, d.Members[n])
		}
		if d.OwnBump != "" && d.OwnBump != "none" {
			fmt.Printf("  highest bump among released members: %s\n", d.OwnBump)
		}
	} else {
		base := "the beginning of history"
		if d.BaseTag != "" {
			base = d.BaseTag
		}
		fmt.Printf("  counting commits since: %s\n\n", base)
		if len(d.Considered) == 0 {
			fmt.Println("  commits that count for this component: none")
		} else {
			fmt.Println("  commits that count for this component:")
			for _, c := range d.Considered {
				sha := c.SHA
				if len(sha) > 7 {
					sha = sha[:7]
				}
				verdict := c.Bump
				switch {
				case c.Skipped:
					verdict = "ignored ([skip release])"
				case !c.Fresh:
					verdict += " (already in the latest pre-release)"
				}
				fmt.Printf("    %s  %-44s %-10s %s\n", sha, truncate(c.Subject, 44), "via "+c.Via, verdict)
			}
		}
		fmt.Printf("\n  highest bump from commits: %s\n", d.OwnBump)
		if d.Forced != "" {
			fmt.Printf("  forced: %s (--force)\n", d.Forced)
		}
		if len(d.CascadeFrom) > 0 {
			fmt.Printf("  cascade: %s released, so this component gets at least %s\n", strings.Join(d.CascadeFrom, ", "), d.CascadeBump)
		}
	}
	fmt.Println()
	if d.Released {
		fmt.Printf("  result: release %s -> %s (tag %s)\n", orNew(d.Current), d.Next, d.Tag)
	} else {
		fmt.Printf("  result: no release: %s\n", d.Reason)
	}
}

func orNew(v string) string {
	if v == "" {
		return "(new)"
	}
	return v
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// newGenerator builds the notes generator; commit links need the repository's web URL, taken from
// the config or derived from the remote (credentials in the remote URL are never copied).
func newGenerator(cfg *config.Config, g *git.CLI, remote string) *notes.Generator {
	url := cfg.RepositoryURL
	if url == "" {
		url = notes.WebURL(g.RemoteURL(remote))
	}
	return &notes.Generator{Cfg: cfg, Git: g, RepoURL: strings.TrimRight(url, "/"), Style: linkStyle()}
}

// cmdNotes prints release notes, or writes the files configured in the "notes" section.
func cmdNotes(cfg *config.Config, g *git.CLI, o options) error {
	if len(o.args) > 2 {
		return fmt.Errorf("usage: monorelease notes [name] [version]")
	}
	gen := newGenerator(cfg, g, o.remote)
	if len(o.args) == 0 || o.write {
		if len(cfg.Notes) == 0 {
			return fmt.Errorf(`no notes configured: add a "notes" section to the config, or print notes with "monorelease notes <name>"`)
		}
		name := ""
		if len(o.args) > 0 {
			name = o.args[0]
		}
		files, err := gen.Write(o.dir, name)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			fmt.Fprintln(os.Stderr, "nothing to write: no releases yet")
		}
		for _, f := range files {
			fmt.Println("wrote", f)
		}
		return nil
	}
	name, version := o.args[0], ""
	if len(o.args) == 2 {
		version = strings.TrimPrefix(o.args[1], cfg.TagPrefix(name))
	}
	var out string
	var err error
	if o.all {
		out, err = gen.Changelog(name)
	} else {
		out, err = gen.Notes(name, version)
	}
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// linkStyle tells GitHub from GitLab links: MONO_RELEASE_FORGE, else the CI environment, else "" (guess from the host).
func linkStyle() string {
	if f := strings.ToLower(os.Getenv("MONO_RELEASE_FORGE")); f == "github" || f == "gitlab" {
		return f
	}
	switch {
	case os.Getenv("GITLAB_CI") != "" || os.Getenv("CI_SERVER_URL") != "":
		return "gitlab"
	case os.Getenv("GITHUB_ACTIONS") != "":
		return "github"
	}
	return ""
}
