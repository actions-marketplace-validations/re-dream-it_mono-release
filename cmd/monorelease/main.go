package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/re-dream-it/mono-release/internal/commits"
	"github.com/re-dream-it/mono-release/internal/config"
	"github.com/re-dream-it/mono-release/internal/git"
	"github.com/re-dream-it/mono-release/internal/planner"
)

const usage = `usage: monorelease <command> [args] [flags]

commands:
  plan                         show which components will be released and their next versions
  release                      create tags for the plan (--push to push them, and to create GitHub/GitLab releases)
  version [name]               latest released version: of one component or umbrella, or of all of them
  manifest <umbrella> [ver]    components and versions of an umbrella release (default: the latest)
  changed                      components with changes (for CI: what to build and test)
  explain <name>               why a component or umbrella is, or is not, released
  notes [name] [ver]           release notes; without a name, writes the files configured in "notes"
  completion <shell>           print a shell completion script (bash, zsh or fish)

flags:
  -v, --version      print the version of monorelease
  -c <file>          config path (default mono-release.yaml)
  -C <dir>           repository directory (default .)
  --branch <name>    branch to work from (default: $CI_COMMIT_BRANCH, else the checked-out branch)
  --ref <ref>        read this branch, tag or commit instead of the checked-out one (not for release)
  --json             JSON output
  --stable           version: only stable versions, whatever the branch
  --component <name> manifest: print only this component's version
  --force <name>[=bump]  plan/release: release the component even without significant commits
                     (bump is patch, minor or major; default patch). May be repeated.
  --base <ref>       changed: compare with where the current ref diverged from this ref (merge requests)
  --with-dependents  changed: also list the components that depend on changed ones
  --write            notes <name>: write the configured files for that name instead of printing
  --all              notes <name>: print the changelog of every release
  --push             release: push the tags
  --remote <name>    remote for --push and for commit links (default origin)
  --no-notes         release: do not write the configured notes files
  --no-releases      release: do not create GitHub/GitLab releases
`

type options struct {
	cfgPath, dir, branch, ref, remote, component, base string
	asJSON, push, stable, withDependents               bool
	write, all, noNotes, noReleases                    bool
	force                                              multiFlag
	args                                               []string // positional arguments
}

// multiFlag is a string flag that may be given several times.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-version" || os.Args[1] == "-v") {
		fmt.Println(versionString())
		return
	}
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
	fs.StringVar(&o.ref, "ref", "", "revision to read")
	fs.BoolVar(&o.push, "push", false, "push tags")
	fs.BoolVar(&o.stable, "stable", false, "ignore prerelease versions")
	fs.StringVar(&o.remote, "remote", "origin", "remote")
	fs.StringVar(&o.component, "component", "", "manifest: show only this component")
	fs.StringVar(&o.base, "base", "", "changed: base ref")
	fs.BoolVar(&o.withDependents, "with-dependents", false, "changed: include dependents")
	fs.BoolVar(&o.write, "write", false, "notes: write files")
	fs.BoolVar(&o.all, "all", false, "notes: whole changelog")
	fs.BoolVar(&o.noNotes, "no-notes", false, "release: skip notes files")
	fs.BoolVar(&o.noReleases, "no-releases", false, "release: skip GitHub/GitLab releases")
	fs.Var(&o.force, "force", "force a release: name or name=bump")

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

// parseForce turns "--force orders" / "--force orders=minor" into a map.
func parseForce(specs []string) (map[string]commits.Bump, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	m := map[string]commits.Bump{}
	for _, spec := range specs {
		name, bump, hasBump := strings.Cut(spec, "=")
		b := commits.Patch
		if hasBump {
			switch bump {
			case "patch":
			case "minor":
				b = commits.Minor
			case "major":
				b = commits.Major
			default:
				return nil, fmt.Errorf("--force %s: bump must be patch, minor or major", spec)
			}
		}
		if name == "" {
			return nil, fmt.Errorf("--force: empty component name in %q", spec)
		}
		m[name] = b
	}
	return m, nil
}

// refBranch guesses the branch behind --ref: "origin/beta" and "refs/heads/beta" both mean "beta".
func refBranch(ref string) string {
	for _, p := range []string{"refs/remotes/origin/", "refs/heads/", "origin/"} {
		if r, ok := strings.CutPrefix(ref, p); ok {
			return r
		}
	}
	return ref
}

// setup loads the config and works out the release channel and stable refs for the branch.
//
// Commands that only read (changed, notes) must work on any branch, such as a merge request's feature
// branch, so for them an unlisted branch is not an error: they fall back to stable.
func setup(cmd string, o options) (*config.Config, *git.CLI, planner.Options, error) {
	cfg, err := config.Load(configPath(o.cfgPath))
	if err != nil {
		return nil, nil, planner.Options{}, err
	}
	g := git.New(o.dir)
	g.Ref = o.ref
	force, err := parseForce(o.force)
	if err != nil {
		return nil, nil, planner.Options{}, err
	}
	if o.stable {
		// stable versions only: the branch does not matter, but releases made on the stable
		// branches count even if the current branch never merged them
		opt := planner.Options{Force: force}
		for _, b := range cfg.StableBranches() {
			opt.StableRefs = append(opt.StableRefs, g.BranchRefs(b)...)
		}
		return cfg, g, opt, nil
	}
	branch := o.branch
	if branch == "" && o.ref != "" {
		branch = refBranch(o.ref)
	}
	if branch == "" {
		branch = os.Getenv("CI_COMMIT_BRANCH") // GitLab CI checks out a detached HEAD
	}
	if branch == "" {
		if branch, err = g.CurrentBranch(); err != nil {
			return nil, nil, planner.Options{}, err
		}
	}
	channel, err := cfg.ChannelFor(branch)
	if err != nil && (cmd == "changed" || cmd == "notes") {
		channel, err = "", nil
	}
	if err != nil {
		if o.ref != "" && o.branch == "" {
			err = fmt.Errorf("%w (--ref %s: say which release branch it belongs to with --branch)", err, o.ref)
		}
		return nil, nil, planner.Options{}, err
	}
	opt := planner.Options{Channel: channel, Force: force}
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
	case "completion":
		return cmdCompletion(o.args)
	case "__names": // used by the completion scripts
		cfg, err := config.Load(configPath(o.cfgPath))
		if err != nil {
			return err
		}
		for _, n := range append(cfg.Names(), cfg.UmbrellaNames()...) {
			fmt.Println(n)
		}
		return nil
	case "plan", "release", "version", "manifest", "changed", "explain", "notes":
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
	if cmd == "release" && o.ref != "" {
		return fmt.Errorf("release works on the checked-out commit; --ref is only for reading")
	}
	cfg, g, opt, err := setup(cmd, o)
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
	case "changed":
		return cmdChanged(cfg, g, opt, o)
	case "explain":
		return cmdExplain(cfg, g, opt, o)
	case "notes":
		return cmdNotes(cfg, g, o)
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
	return cmdRelease(cfg, g, plan, o)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
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
		if r.Forced {
			extra += ", forced"
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
