package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"mono-release/internal/config"
	"mono-release/internal/git"
	"mono-release/internal/planner"
)

const usage = `usage: monorelease <command> [args] [flags]

commands:
  plan                         show which components will be released and their next versions
  release                      create tags for the plan (add --push to push them)
  version [name]               latest released version: of one component or umbrella, or of all of them
  manifest <umbrella> [ver]    components and versions of an umbrella release (default: the latest);
                               with --component <name> prints only that component's version

flags:
  -c <file>   config path (default mono-release.yaml; semver.yaml is still accepted)
  -C <dir>    repository directory (default .)
  --branch    branch to work from (default: $CI_COMMIT_BRANCH, else the checked-out branch)
  --json      JSON output (plan, version, manifest)
  --stable    version: ignore prerelease versions, whatever the branch
  --component manifest: show only this component (prints just its version)
  --push      push tags (release)
  --remote    remote for --push (default origin)
`

type options struct {
	cfgPath, dir, branch, remote, component string
	asJSON, push, stable                    bool
	args                                    []string // positional arguments
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	var o options
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.StringVar(&o.cfgPath, "c", "", "config path")
	fs.StringVar(&o.dir, "C", ".", "repository directory")
	fs.BoolVar(&o.asJSON, "json", false, "JSON output")
	fs.StringVar(&o.branch, "branch", "", "branch to work from")
	fs.BoolVar(&o.push, "push", false, "push tags")
	fs.BoolVar(&o.stable, "stable", false, "ignore prerelease versions")
	fs.StringVar(&o.remote, "remote", "origin", "remote for --push")
	fs.StringVar(&o.component, "component", "", "manifest: show only this component")

	// allow flags after positional arguments: "monorelease version orders --json"
	rest := os.Args[2:]
	for {
		fs.Parse(rest)
		if fs.NArg() == 0 {
			break
		}
		o.args = append(o.args, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	if err := run(cmd, o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// configPath picks the config file: the -c flag, else mono-release.yaml, else the legacy semver.yaml.
func configPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if _, err := os.Stat("mono-release.yaml"); err != nil {
		if _, err := os.Stat("semver.yaml"); err == nil {
			fmt.Fprintln(os.Stderr, "warning: using legacy semver.yaml; rename it to mono-release.yaml")
			return "semver.yaml"
		}
	}
	return "mono-release.yaml"
}

// setup loads the config and works out the release channel and stable refs for the branch.
func setup(o options) (*config.Config, *git.CLI, planner.Options, error) {
	cfg, err := config.Load(configPath(o.cfgPath))
	if err != nil {
		return nil, nil, planner.Options{}, err
	}
	g := git.New(o.dir)
	if o.stable {
		// stable versions only: the branch does not matter, but releases made on the stable
		// branches count even if the current branch never merged them
		opt := planner.Options{}
		for _, b := range cfg.StableBranches() {
			opt.StableRefs = append(opt.StableRefs, g.BranchRefs(b)...)
		}
		return cfg, g, opt, nil
	}
	branch := o.branch
	if branch == "" {
		branch = os.Getenv("CI_COMMIT_BRANCH") // GitLab CI checks out a detached HEAD
	}
	if branch == "" {
		if branch, err = g.CurrentBranch(); err != nil {
			return nil, nil, planner.Options{}, err
		}
	}
	channel, err := cfg.ChannelFor(branch)
	if err != nil {
		return nil, nil, planner.Options{}, err
	}
	opt := planner.Options{Channel: channel}
	if channel != "" {
		// stable releases made on the stable branch(es) must be visible even if never merged back
		for _, b := range cfg.StableBranches() {
			refs := g.BranchRefs(b)
			if len(refs) == 0 {
				return nil, nil, opt, fmt.Errorf("stable branch %q not found locally or in origin; fetch it (e.g. git fetch origin %s)", b, b)
			}
			opt.StableRefs = append(opt.StableRefs, refs...)
		}
	}
	return cfg, g, opt, nil
}

func run(cmd string, o options) error {
	switch cmd {
	case "plan", "release", "version", "manifest":
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
	cfg, g, opt, err := setup(o)
	if err != nil {
		return err
	}
	if git.TraceEnabled() {
		defer g.PrintSummary(os.Stderr)
	}
	switch cmd {
	case "version":
		return cmdVersion(cfg, g, opt, o)
	case "manifest":
		return cmdManifest(cfg, g, opt, o)
	}

	plan, err := planner.Build(cfg, g, opt)
	if err != nil {
		return err
	}
	if cmd == "plan" {
		if o.asJSON {
			return printJSON(plan)
		}
		printPlan(plan)
		return nil
	}

	// release: component tags first, then umbrella tags (their message holds the member versions)
	var names []string
	for _, r := range plan.Releases {
		if err := g.CreateTag(r.Tag, plan.SHA, ""); err != nil {
			return err
		}
		fmt.Println("tagged", r.Tag)
		names = append(names, r.Tag)
	}
	for _, u := range plan.Umbrellas {
		if err := g.CreateTag(u.Tag, plan.SHA, u.Manifest()); err != nil {
			return err
		}
		fmt.Println("tagged", u.Tag)
		names = append(names, u.Tag)
	}
	if len(names) == 0 {
		fmt.Println("nothing to release")
		return nil
	}
	if o.push {
		// a single atomic push: either all tags reach the remote or none
		if err := g.PushTags(o.remote, names); err != nil {
			return err
		}
		fmt.Printf("pushed %d tags to %s\n", len(names), o.remote)
	}
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

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
		ver = strings.TrimPrefix(o.args[1], name+"@")
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
	tag := name + "@" + ver
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

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ") + "\n"
}

func printPlan(p *planner.Plan) {
	if len(p.Releases) == 0 {
		fmt.Println("nothing to release")
		return
	}
	for _, r := range p.Releases {
		from := r.Current
		if from == "" {
			from = "(new)"
		}
		extra := fmt.Sprintf("%d commits", len(r.Changes))
		if len(r.CascadeFrom) > 0 {
			extra += ", cascade from " + strings.Join(r.CascadeFrom, ",")
		}
		fmt.Printf("%s: %s -> %s [%s, %s]\n", r.Component, from, r.Next, r.Bump, extra)
	}
	for _, u := range p.Umbrellas {
		from := u.Current
		if from == "" {
			from = "(new)"
		}
		fmt.Printf("umbrella %s: %s -> %s [%s]\n", u.Name, from, u.Next, u.Bump)
		fmt.Print(indent(u.Manifest()))
	}
}
