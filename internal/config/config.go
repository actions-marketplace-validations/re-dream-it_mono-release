// Package config loads and validates mono-release.yaml.
package config

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"

	"github.com/re-dream-it/mono-release/internal/graph"
)

type Config struct {
	// InitialVersion is used for a component's first release.
	InitialVersion string `yaml:"initial_version"`
	// Releases says for which tags a GitHub/GitLab Release is created: umbrella, components, all or none.
	// Empty means umbrella when the config has umbrellas, otherwise none.
	Releases string `yaml:"releases"`
	// RepositoryURL is the web URL used for commit links in release notes; derived from the remote when empty.
	RepositoryURL string `yaml:"repository_url"`
	// Notes lists which release notes are generated and where they are written.
	Notes []NotesEntry `yaml:"notes"`
	// Branches maps release branches to channels. Empty means every branch releases stable versions.
	Branches   []Branch             `yaml:"branches"`
	Components map[string]Component `yaml:"components"`
	Umbrellas  map[string]Umbrella  `yaml:"umbrellas"`
}

// NotesEntry configures generated release notes: for which component or umbrella, and into which file.
type NotesEntry struct {
	// For is a component or umbrella name, or "*" for all of them.
	For string `yaml:"for"`
	// File is the output path relative to the repository. {name} and {version} are replaced.
	File string `yaml:"file"`
	// History is "latest" (default: only the newest release) or "all" (a changelog of every release).
	History string `yaml:"history"`
}

// Branch describes a release branch. Name may be a glob ("release/*").
// Prerelease is the channel id (e.g. "beta"); empty means stable releases.
type Branch struct {
	Name       string `yaml:"name"`
	Prerelease string `yaml:"prerelease"`
}

var channelRe = regexp.MustCompile(`^[0-9A-Za-z-]+$`)

// ChannelFor returns the prerelease channel for a branch ("" = stable).
// With no branches configured every branch is stable; otherwise an unlisted branch is an error.
func (c *Config) ChannelFor(branch string) (string, error) {
	if len(c.Branches) == 0 {
		return "", nil
	}
	for _, b := range c.Branches {
		if ok, _ := doublestar.Match(b.Name, branch); ok {
			return b.Prerelease, nil
		}
	}
	return "", fmt.Errorf("branch %q is not configured for releases (see branches in config)", branch)
}

// StableBranches returns the explicitly named (non-glob) stable branches.
func (c *Config) StableBranches() []string {
	var names []string
	for _, b := range c.Branches {
		if b.Prerelease == "" && !strings.ContainsAny(b.Name, "*?[{") {
			names = append(names, b.Name)
		}
	}
	return names
}

// Umbrella is a versioned set of components. Its version is bumped by the highest
// bump among the released members; the member versions are stored in the tag message.
type Umbrella struct {
	Members []string `yaml:"members"`
	// All makes the umbrella cover every component (mutually exclusive with Members).
	All bool `yaml:"all"`
}

// MemberNames returns the umbrella's components in stable order.
func (c *Config) MemberNames(u Umbrella) []string {
	if u.All {
		return c.Names()
	}
	return u.Members
}

type Component struct {
	Paths     []string `yaml:"paths"`
	Exclude   []string `yaml:"exclude"`
	DependsOn []string `yaml:"depends_on"`
	// Cascade is the bump applied when a dependency is released: patch (default), minor or none.
	Cascade string `yaml:"cascade"`
	// Scopes are extra Conventional Commit scopes that point at this component, besides its own name:
	// "feat(api): ..." counts for the component when "api" is listed here, even outside its paths.
	Scopes []string `yaml:"scopes"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func Parse(data []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.InitialVersion == "" {
		c.InitialVersion = "1.0.0"
	}
	if err := c.normalizePatterns(); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Names returns component names in stable order.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Components))
	for n := range c.Components {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c *Config) validate() error {
	if len(c.Components) == 0 {
		return fmt.Errorf("config: no components defined")
	}
	switch c.Releases {
	case "", "umbrella", "components", "all", "none":
	default:
		return fmt.Errorf("releases must be umbrella, components, all or none, got %q", c.Releases)
	}
	for _, name := range c.Names() {
		comp := c.Components[name]
		if strings.ContainsAny(name, "@ /\t") {
			return fmt.Errorf("component %q: name must not contain '@', '/' or whitespace", name)
		}
		if len(comp.Paths) == 0 {
			return fmt.Errorf("component %q: paths must not be empty", name)
		}
		switch comp.Cascade {
		case "", "patch", "minor", "none":
		default:
			return fmt.Errorf("component %q: cascade must be patch, minor or none", name)
		}
		for _, d := range comp.DependsOn {
			if _, ok := c.Components[d]; !ok {
				return fmt.Errorf("component %q: unknown dependency %q", name, d)
			}
			if d == name {
				return fmt.Errorf("component %q: depends on itself", name)
			}
		}
	}
	for i, b := range c.Branches {
		if b.Name == "" {
			return fmt.Errorf("branches[%d]: name must not be empty", i)
		}
		if b.Prerelease != "" && !channelRe.MatchString(b.Prerelease) {
			return fmt.Errorf("branch %q: prerelease must contain only letters, digits and '-'", b.Name)
		}
	}
	if _, err := graph.Topo(c.Deps()); err != nil {
		return err
	}
	if err := c.validateNotes(); err != nil {
		return err
	}
	for name, u := range c.Umbrellas {
		if strings.ContainsAny(name, "@ /\t") {
			return fmt.Errorf("umbrella %q: name must not contain '@', '/' or whitespace", name)
		}
		if _, clash := c.Components[name]; clash {
			return fmt.Errorf("umbrella %q: name clashes with a component", name)
		}
		if u.All && len(u.Members) > 0 {
			return fmt.Errorf("umbrella %q: use either all: true or members, not both", name)
		}
		if !u.All && len(u.Members) == 0 {
			return fmt.Errorf("umbrella %q: members must not be empty (or set all: true)", name)
		}
		for _, m := range u.Members {
			if _, ok := c.Components[m]; !ok {
				return fmt.Errorf("umbrella %q: unknown member %q", name, m)
			}
		}
	}
	return nil
}

// Deps returns the dependency map (component -> its dependencies).
func (c *Config) Deps() map[string][]string {
	m := make(map[string][]string, len(c.Components))
	for n, comp := range c.Components {
		m[n] = comp.DependsOn
	}
	return m
}

// UmbrellaNames returns umbrella names in stable order.
func (c *Config) UmbrellaNames() []string {
	names := make([]string, 0, len(c.Umbrellas))
	for n := range c.Umbrellas {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Tags are always named "<name>@<version>", for components and umbrellas alike. The format is fixed on
// purpose: everything that looks versions up (the planner, version, manifest, notes) relies on it.

// TagName returns the tag name for a component or umbrella version: "orders@1.4.0".
func (c *Config) TagName(name, version string) string {
	return c.TagPrefix(name) + version
}

// TagPrefix returns the part of the tag name before the version: "orders@".
func (c *Config) TagPrefix(name string) string {
	return name + "@"
}

// ScopeMatches reports whether one of the commit scopes points at the component: its own name or an alias.
func (c *Config) ScopeMatches(component string, scopes []string) bool {
	comp := c.Components[component]
	for _, sc := range scopes {
		if sc == component {
			return true
		}
		for _, alias := range comp.Scopes {
			if sc == alias {
				return true
			}
		}
	}
	return false
}

// ReleaseTargets resolves the "releases" setting: which tags get a GitHub/GitLab Release.
func (c *Config) ReleaseTargets() (umbrellas, components bool) {
	switch c.Releases {
	case "umbrella":
		return true, false
	case "components":
		return false, true
	case "all":
		return true, true
	case "none":
		return false, false
	}
	return len(c.Umbrellas) > 0, false // default: umbrellas only, and only if there are any
}

func (c *Config) validateNotes() error {
	for i, n := range c.Notes {
		where := fmt.Sprintf("notes[%d]", i)
		if n.For != "*" {
			_, isComp := c.Components[n.For]
			_, isUmb := c.Umbrellas[n.For]
			if !isComp && !isUmb {
				return fmt.Errorf("%s: unknown component or umbrella %q", where, n.For)
			}
		}
		if n.File == "" {
			return fmt.Errorf("%s: file must not be empty", where)
		}
		if path.IsAbs(n.File) || n.File != path.Clean(n.File) || strings.HasPrefix(n.File, "..") {
			return fmt.Errorf("%s: file %q must be a clean relative path inside the repository", where, n.File)
		}
		switch n.History {
		case "", "latest", "all":
		default:
			return fmt.Errorf("%s: history must be latest or all, got %q", where, n.History)
		}
		if n.For == "*" && !strings.Contains(n.File, "{name}") {
			return fmt.Errorf("%s: file must contain {name} when for is \"*\"", where)
		}
		if n.History == "all" && strings.Contains(n.File, "{version}") {
			return fmt.Errorf("%s: file must not contain {version} when history is all", where)
		}
	}
	return nil
}

// normalizePatterns cleans the glob patterns of every component so that common spellings work:
// "./services/orders/**" is the same as "services/orders/**". Patterns are relative to the repository
// root, so an absolute pattern or one that climbs out of it can never match and is rejected.
func (c *Config) normalizePatterns() error {
	for _, name := range c.Names() {
		comp := c.Components[name]
		for _, list := range []*[]string{&comp.Paths, &comp.Exclude} {
			for i, p := range *list {
				p = strings.TrimSpace(p)
				for strings.HasPrefix(p, "./") {
					p = strings.TrimPrefix(p, "./")
				}
				if strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
					return fmt.Errorf("component %q: pattern %q must be relative to the repository root", name, (*list)[i])
				}
				if p == "" || p == "." {
					return fmt.Errorf("component %q: empty pattern %q", name, (*list)[i])
				}
				(*list)[i] = p
			}
		}
		c.Components[name] = comp
	}
	return nil
}
