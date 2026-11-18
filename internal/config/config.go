// Package config loads and validates mono-release.yaml.
package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"

	"mono-release/internal/graph"
)

type Config struct {
	// InitialVersion is used for a component's first release.
	InitialVersion string `yaml:"initial_version"`
	// Branches maps release branches to channels. Empty means every branch releases stable versions.
	Branches   []Branch             `yaml:"branches"`
	Components map[string]Component `yaml:"components"`
	Umbrellas  map[string]Umbrella  `yaml:"umbrellas"`
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
