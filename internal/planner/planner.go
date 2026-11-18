// Package planner computes a release plan. It performs no writes.
package planner

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/bmatcuk/doublestar/v4"

	"mono-release/internal/commits"
	"mono-release/internal/config"
	"mono-release/internal/git"
	"mono-release/internal/graph"
)

type Change struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Type    string `json:"type,omitempty"`
	Bump    string `json:"bump"`
}

type Release struct {
	Component   string   `json:"component"`
	Current     string   `json:"current,omitempty"` // latest version on this channel; empty on first release
	Next        string   `json:"next"`
	Tag         string   `json:"tag"`
	Bump        string   `json:"bump"`
	Channel     string   `json:"channel,omitempty"` // prerelease channel; empty for stable
	Changes     []Change `json:"changes,omitempty"`
	CascadeFrom []string `json:"cascade_from,omitempty"` // released dependencies that triggered a cascade bump
}

type UmbrellaRelease struct {
	Name    string            `json:"name"`
	Current string            `json:"current,omitempty"`
	Next    string            `json:"next"`
	Tag     string            `json:"tag"`
	Bump    string            `json:"bump"`
	Channel string            `json:"channel,omitempty"`
	Members map[string]string `json:"members"` // component -> version included in this umbrella
}

// Manifest is the text stored in the umbrella tag message: one "component@version" per line.
func (u UmbrellaRelease) Manifest() string {
	names := make([]string, 0, len(u.Members))
	for n := range u.Members {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s@%s\n", n, u.Members[n])
	}
	return b.String()
}

// ParseManifest reads an umbrella tag message (one "component@version" per line).
func ParseManifest(msg string) (map[string]string, error) {
	members := map[string]string{}
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		comp, ver, ok := strings.Cut(line, "@")
		if !ok || comp == "" || ver == "" {
			return nil, fmt.Errorf("bad manifest line %q (want component@version)", line)
		}
		members[comp] = ver
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("empty manifest")
	}
	return members, nil
}

// Versions holds the latest released version of every component and umbrella that has one.
// Anything not released yet is absent.
type Versions struct {
	Components map[string]string `json:"components"`
	Umbrellas  map[string]string `json:"umbrellas,omitempty"`
}

// Latest returns the latest released versions as the planner sees them: tags reachable from HEAD
// plus (on a prerelease channel) the stable branches' tags. With an empty channel only stable
// versions count.
func Latest(cfg *config.Config, r git.Reader, opt Options) (*Versions, error) {
	v := &Versions{Components: map[string]string{}, Umbrellas: map[string]string{}}
	one := func(name string, into map[string]string) error {
		tags, stableTags, err := opt.tags(r, name)
		if err != nil {
			return err
		}
		if cur := resolveState(tags, stableTags, opt.Channel).latest(); cur != nil {
			into[name] = cur.String()
		}
		return nil
	}
	for _, n := range cfg.Names() {
		if err := one(n, v.Components); err != nil {
			return nil, err
		}
	}
	for _, n := range cfg.UmbrellaNames() {
		if err := one(n, v.Umbrellas); err != nil {
			return nil, err
		}
	}
	return v, nil
}

type Plan struct {
	SHA       string            `json:"sha"`
	Channel   string            `json:"channel,omitempty"`
	Releases  []Release         `json:"releases"`
	Umbrellas []UmbrellaRelease `json:"umbrellas,omitempty"`
}

// tagState is what the tags say about a component (or umbrella) on a given channel.
type tagState struct {
	stable    *semver.Version // highest stable version
	stableTag string
	pre       *semver.Version // highest prerelease of this channel that is newer than stable
	preTag    string
}

func (s tagState) latest() *semver.Version {
	if s.pre != nil {
		return s.pre
	}
	return s.stable
}

func (s tagState) latestTag() string {
	if s.pre != nil {
		return s.preTag
	}
	return s.stableTag
}

// resolveState combines tags reachable from HEAD with stable tags from the stable branches,
// so a stable release made elsewhere is seen even if it was never merged into this branch.
func resolveState(tags, stableTags []git.Tag, channel string) tagState {
	var st tagState
	for _, t := range append(append([]git.Tag(nil), tags...), stableTags...) {
		if t.Version.Prerelease() == "" && (st.stable == nil || t.Version.GreaterThan(st.stable)) {
			st.stable, st.stableTag = t.Version, t.Name
		}
	}
	if channel == "" {
		return st
	}
	for _, t := range tags {
		if _, ok := preNumber(t.Version, channel); !ok {
			continue
		}
		if st.stable != nil && !core(t.Version).GreaterThan(st.stable) {
			continue // stable has caught up with this prerelease line
		}
		if st.pre == nil || t.Version.GreaterThan(st.pre) {
			st.pre, st.preTag = t.Version, t.Name
		}
	}
	return st
}

// preNumber extracts N from a "<channel>.N" prerelease.
func preNumber(v *semver.Version, channel string) (int, bool) {
	rest, ok := strings.CutPrefix(v.Prerelease(), channel+".")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil
}

func core(v *semver.Version) *semver.Version {
	c, _ := semver.NewVersion(fmt.Sprintf("%d.%d.%d", v.Major(), v.Minor(), v.Patch()))
	return c
}

// nextVersion computes the version to release. On a channel the core version never goes
// below an existing prerelease line, and the prerelease counter continues that line.
func nextVersion(cfg *config.Config, st tagState, b commits.Bump, channel string) (*semver.Version, error) {
	var c *semver.Version
	if st.stable != nil {
		c = core(ptr(bump(*st.stable, b)))
	} else {
		v, err := semver.NewVersion(cfg.InitialVersion)
		if err != nil {
			return nil, fmt.Errorf("initial_version: %w", err)
		}
		c = core(v)
	}
	if channel == "" {
		return c, nil
	}
	n := 1
	if st.pre != nil {
		pc := core(st.pre)
		if pc.GreaterThan(c) {
			c = pc
		}
		if pc.Equal(c) {
			n, _ = preNumber(st.pre, channel)
			n++
		}
	}
	return semver.NewVersion(fmt.Sprintf("%s-%s.%d", c, channel, n))
}

func ptr(v semver.Version) *semver.Version { return &v }

// Options describes the branch being released from.
type Options struct {
	// Channel is the prerelease channel of the current branch ("" = stable).
	Channel string
	// StableRefs are refs of the stable branches; stable tags reachable from them count as
	// released even when they are not in the current branch's history.
	StableRefs []string
}

// tags returns the component's tags: those reachable from HEAD and the stable ones from StableRefs.
func (o Options) tags(r git.Reader, name string) (head, stable []git.Tag, err error) {
	if head, err = r.Tags(name, "HEAD"); err != nil {
		return nil, nil, err
	}
	for _, ref := range o.StableRefs {
		ts, err := r.Tags(name, ref)
		if err != nil {
			return nil, nil, err
		}
		for _, t := range ts {
			if t.Version.Prerelease() == "" {
				stable = append(stable, t)
			}
		}
	}
	return head, stable, nil
}

// Build computes which components need a release and their next versions.
func Build(cfg *config.Config, r git.Reader, opt Options) (*Plan, error) {
	channel := opt.Channel
	head, err := r.Head()
	if err != nil {
		return nil, err
	}
	plan := &Plan{SHA: head, Channel: channel}

	states := map[string]tagState{}
	rels := map[string]*Release{}
	bumps := map[string]commits.Bump{}

	// 0. tag state of every component, then load all needed history in one pass
	for _, name := range cfg.Names() {
		tags, stableTags, err := opt.tags(r, name)
		if err != nil {
			return nil, err
		}
		states[name] = resolveState(tags, stableTags, channel)
	}
	if hl, ok := r.(git.HistoryLoader); ok {
		var bases []string
		for _, name := range cfg.Names() {
			st := states[name]
			bases = append(bases, st.stableTag)
			if st.pre != nil {
				bases = append(bases, st.preTag)
			}
		}
		if err := hl.LoadHistory(bases); err != nil {
			return nil, err
		}
	}

	// 1. direct changes
	for _, name := range cfg.Names() {
		comp := cfg.Components[name]
		st := states[name]

		// bump is cumulative since the last stable release; "fresh" commits are those since the latest tag
		all, err := r.Commits(st.stableTag)
		if err != nil {
			return nil, err
		}
		fresh := map[string]bool{}
		if st.pre != nil {
			newer, err := r.Commits(st.preTag)
			if err != nil {
				return nil, err
			}
			for _, c := range newer {
				fresh[c.SHA] = true
			}
		}

		var changes []Change
		max, freshMax := commits.None, commits.None
		for _, c := range all {
			if !touches(comp, c.Files) {
				continue
			}
			p := commits.Parse(c.Subject, c.Body)
			if p.Bump > max {
				max = p.Bump
			}
			if st.pre == nil || fresh[c.SHA] {
				changes = append(changes, Change{SHA: c.SHA, Subject: c.Subject, Type: p.Type, Bump: p.Bump.String()})
				if p.Bump > freshMax {
					freshMax = p.Bump
				}
			}
		}
		if freshMax == commits.None {
			continue
		}
		rel, err := newRelease(cfg, name, st, max, channel)
		if err != nil {
			return nil, err
		}
		rel.Changes = changes
		rels[name], bumps[name] = rel, max
	}

	// 2. cascade through the dependency graph (dependencies first)
	order, err := graph.Topo(cfg.Deps())
	if err != nil {
		return nil, err
	}
	for _, name := range order {
		comp := cfg.Components[name]
		st := states[name]
		if comp.Cascade == "none" || st.latest() == nil {
			continue // components that were never released have no version to bump
		}
		var from []string
		for _, d := range comp.DependsOn {
			if rels[d] != nil {
				from = append(from, d)
			}
		}
		if len(from) == 0 {
			continue
		}
		sort.Strings(from)
		cb := commits.Patch
		if comp.Cascade == "minor" {
			cb = commits.Minor
		}
		if rel := rels[name]; rel != nil {
			rel.CascadeFrom = from
			if cb > bumps[name] {
				nr, err := newRelease(cfg, name, st, cb, channel)
				if err != nil {
					return nil, err
				}
				nr.Changes, nr.CascadeFrom = rel.Changes, from
				rels[name], bumps[name] = nr, cb
			}
			continue
		}
		nr, err := newRelease(cfg, name, st, cb, channel)
		if err != nil {
			return nil, err
		}
		nr.CascadeFrom = from
		rels[name], bumps[name] = nr, cb
	}

	for _, name := range cfg.Names() {
		if rel := rels[name]; rel != nil {
			plan.Releases = append(plan.Releases, *rel)
		}
	}

	// 3. umbrellas: released when any member is released
	for _, uname := range cfg.UmbrellaNames() {
		u := cfg.Umbrellas[uname]
		max := commits.None
		members := map[string]string{}
		for _, m := range cfg.MemberNames(u) {
			if rel := rels[m]; rel != nil {
				members[m] = rel.Next
				if bumps[m] > max {
					max = bumps[m]
				}
			} else if v := states[m].latest(); v != nil {
				members[m] = v.String()
			}
		}
		if max == commits.None {
			continue
		}
		tags, stableTags, err := opt.tags(r, uname)
		if err != nil {
			return nil, err
		}
		ur, err := newRelease(cfg, uname, resolveState(tags, stableTags, channel), max, channel)
		if err != nil {
			return nil, err
		}
		plan.Umbrellas = append(plan.Umbrellas, UmbrellaRelease{
			Name: uname, Current: ur.Current, Next: ur.Next, Tag: ur.Tag, Bump: ur.Bump, Channel: channel, Members: members,
		})
	}
	return plan, nil
}

func newRelease(cfg *config.Config, name string, st tagState, b commits.Bump, channel string) (*Release, error) {
	next, err := nextVersion(cfg, st, b, channel)
	if err != nil {
		return nil, err
	}
	rel := &Release{Component: name, Bump: b.String(), Channel: channel, Next: next.String(), Tag: name + "@" + next.String()}
	if cur := st.latest(); cur != nil {
		rel.Current = cur.String()
	}
	return rel, nil
}

func bump(v semver.Version, b commits.Bump) semver.Version {
	switch b {
	case commits.Major:
		return v.IncMajor()
	case commits.Minor:
		return v.IncMinor()
	default:
		return v.IncPatch()
	}
}

// touches reports whether any file belongs to the component (matches paths, not excluded).
func touches(c config.Component, files []string) bool {
	for _, f := range files {
		if matchAny(c.Exclude, f) {
			continue
		}
		if matchAny(c.Paths, f) {
			return true
		}
	}
	return false
}

func matchAny(patterns []string, file string) bool {
	for _, p := range patterns {
		if ok, _ := doublestar.Match(p, file); ok {
			return true
		}
	}
	return false
}
