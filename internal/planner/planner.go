// Package planner computes a release plan. It performs no writes.
package planner

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/bmatcuk/doublestar/v4"

	"github.com/re-dream-it/mono-release/internal/commits"
	"github.com/re-dream-it/mono-release/internal/config"
	"github.com/re-dream-it/mono-release/internal/git"
	"github.com/re-dream-it/mono-release/internal/graph"
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
	Forced      bool     `json:"forced,omitempty"`  // released because of --force
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
		tags, stableTags, err := opt.tags(r, cfg.TagPrefix(name))
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
	// Details explains the decision for every component and umbrella; filled only with Options.Explain.
	Details map[string]*Detail `json:"details,omitempty"`
}

// Consideration is one commit that was looked at for a component.
type Consideration struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Via     string `json:"via"`               // paths | scope
	Bump    string `json:"bump"`              // the bump this commit asks for
	Skipped bool   `json:"skipped,omitempty"` // ignored because of [skip release]
	Fresh   bool   `json:"fresh"`             // newer than the latest tag (on a prerelease channel older ones only count towards the bump)
}

// Detail explains why a component or umbrella is, or is not, released.
type Detail struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"` // component | umbrella
	Channel     string            `json:"channel,omitempty"`
	Current     string            `json:"current,omitempty"`
	CurrentTag  string            `json:"current_tag,omitempty"`
	BaseTag     string            `json:"base_tag,omitempty"` // the tag the history is counted from
	Considered  []Consideration   `json:"considered,omitempty"`
	OwnBump     string            `json:"own_bump"`
	Forced      string            `json:"forced,omitempty"`
	CascadeFrom []string          `json:"cascade_from,omitempty"`
	CascadeBump string            `json:"cascade_bump,omitempty"`
	Released    bool              `json:"released"`
	Next        string            `json:"next,omitempty"`
	Tag         string            `json:"tag,omitempty"`
	Reason      string            `json:"reason,omitempty"`  // why there is no release
	Members     map[string]string `json:"members,omitempty"` // umbrella: component -> "1.0.0 -> 1.1.0 (released)" / "1.0.0 (unchanged)"
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
	// Force releases the listed components even without significant commits, with at least this bump.
	Force map[string]commits.Bump
	// Explain fills Plan.Details.
	Explain bool
}

// tags returns the tags named "<prefix><version>": those reachable from the reader's ref and
// the stable ones from StableRefs.
func (o Options) tags(r git.Reader, prefix string) (head, stable []git.Tag, err error) {
	if head, err = r.Tags(prefix, ""); err != nil {
		return nil, nil, err
	}
	for _, ref := range o.StableRefs {
		ts, err := r.Tags(prefix, ref)
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

// Relevance says why a commit counts for a component: "paths" when it touches the component's
// files, "scope" when its Conventional Commit scope names the component (or one of its scope
// aliases), or "" when it does not concern the component at all.
func Relevance(cfg *config.Config, name string, c git.Commit, p commits.Parsed) string {
	if touches(cfg.Components[name], c.Files) {
		return "paths"
	}
	if len(p.Scopes) > 0 && cfg.ScopeMatches(name, p.Scopes) {
		return "scope"
	}
	return ""
}

// Build computes which components need a release and their next versions.
func Build(cfg *config.Config, r git.Reader, opt Options) (*Plan, error) {
	channel := opt.Channel
	for name := range opt.Force {
		if _, ok := cfg.Components[name]; !ok {
			return nil, fmt.Errorf("--force: unknown component %q", name)
		}
	}
	head, err := r.Head()
	if err != nil {
		return nil, err
	}
	plan := &Plan{SHA: head, Channel: channel}
	if opt.Explain {
		plan.Details = map[string]*Detail{}
	}
	detail := func(name, kind string) *Detail {
		if !opt.Explain {
			return nil
		}
		d := &Detail{Name: name, Kind: kind, Channel: channel}
		plan.Details[name] = d
		return d
	}

	states := map[string]tagState{}
	rels := map[string]*Release{}
	bumps := map[string]commits.Bump{}
	details := map[string]*Detail{}

	// 0. tag state of every component, then load all needed history in one pass
	for _, name := range cfg.Names() {
		tags, stableTags, err := opt.tags(r, cfg.TagPrefix(name))
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
		st := states[name]
		det := detail(name, "component")
		details[name] = det
		if det != nil {
			if cur := st.latest(); cur != nil {
				det.Current, det.CurrentTag = cur.String(), st.latestTag()
			}
			det.BaseTag = st.stableTag
		}

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
		skipped := 0
		for _, c := range all {
			if commits.IsMerge(c.Subject) {
				continue // a merge says nothing about the change; the commits it brings in are counted themselves
			}
			p := commits.Parse(c.Subject, c.Body)
			via := Relevance(cfg, name, c, p)
			if via == "" {
				continue
			}
			isFresh := st.pre == nil || fresh[c.SHA]
			if det != nil {
				det.Considered = append(det.Considered, Consideration{
					SHA: c.SHA, Subject: c.Subject, Via: via, Bump: p.Bump.String(), Skipped: p.Skip, Fresh: isFresh})
			}
			if p.Skip {
				skipped++
				continue // "[skip release]": the commit is ignored completely
			}
			if p.Bump > max {
				max = p.Bump
			}
			if isFresh {
				changes = append(changes, Change{SHA: c.SHA, Subject: c.Subject, Type: p.Type, Bump: p.Bump.String()})
				if p.Bump > freshMax {
					freshMax = p.Bump
				}
			}
		}
		if det != nil {
			det.OwnBump = max.String()
		}
		forced, isForced := opt.Force[name]
		if isForced {
			forced = maxBump(forced, commits.Patch)
			if det != nil {
				det.Forced = forced.String()
			}
		}
		if freshMax == commits.None && !isForced {
			if det != nil {
				det.Reason = noReleaseReason(name, st, len(det.Considered), skipped, st.pre != nil && max != commits.None)
			}
			continue
		}
		bumpTo := maxBump(max, forced)
		rel, err := newRelease(cfg, name, st, bumpTo, channel)
		if err != nil {
			return nil, err
		}
		rel.Changes, rel.Forced = changes, isForced
		rels[name], bumps[name] = rel, bumpTo
	}

	// 2. cascade through the dependency graph (dependencies first)
	order, err := graph.Topo(cfg.Deps())
	if err != nil {
		return nil, err
	}
	for _, name := range order {
		comp := cfg.Components[name]
		st := states[name]
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
		det := details[name]
		if comp.Cascade == "none" {
			if det != nil && rels[name] == nil {
				det.Reason = fmt.Sprintf("%s released, but cascade is none for %s", strings.Join(from, ", "), name)
			}
			continue
		}
		if st.latest() == nil {
			if det != nil && rels[name] == nil {
				det.Reason = fmt.Sprintf("%s released, but %s has never been released, so there is no version to bump", strings.Join(from, ", "), name)
			}
			continue
		}
		cb := commits.Patch
		if comp.Cascade == "minor" {
			cb = commits.Minor
		}
		if det != nil {
			det.CascadeFrom, det.CascadeBump = from, cb.String()
		}
		if rel := rels[name]; rel != nil {
			rel.CascadeFrom = from
			if cb > bumps[name] {
				nr, err := newRelease(cfg, name, st, cb, channel)
				if err != nil {
					return nil, err
				}
				nr.Changes, nr.CascadeFrom, nr.Forced = rel.Changes, from, rel.Forced
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
			if det := details[name]; det != nil {
				det.Released, det.Next, det.Tag, det.Reason = true, rel.Next, rel.Tag, ""
			}
		}
	}

	// 3. umbrellas: released when any member is released
	for _, uname := range cfg.UmbrellaNames() {
		u := cfg.Umbrellas[uname]
		det := detail(uname, "umbrella")
		max := commits.None
		members := map[string]string{}
		var memberNotes map[string]string
		if det != nil {
			memberNotes = map[string]string{}
		}
		for _, m := range cfg.MemberNames(u) {
			if rel := rels[m]; rel != nil {
				members[m] = rel.Next
				if bumps[m] > max {
					max = bumps[m]
				}
				if det != nil {
					from := "(first release)"
					if rel.Current != "" {
						from = rel.Current
					}
					memberNotes[m] = fmt.Sprintf("%s -> %s (%s)", from, rel.Next, rel.Bump)
				}
			} else if v := states[m].latest(); v != nil {
				members[m] = v.String()
				if det != nil {
					memberNotes[m] = v.String() + " (unchanged)"
				}
			} else if det != nil {
				memberNotes[m] = "not released yet"
			}
		}
		tags, stableTags, err := opt.tags(r, cfg.TagPrefix(uname))
		if err != nil {
			return nil, err
		}
		ust := resolveState(tags, stableTags, channel)
		if det != nil {
			det.Members, det.OwnBump = memberNotes, max.String()
			if cur := ust.latest(); cur != nil {
				det.Current, det.CurrentTag = cur.String(), ust.latestTag()
			}
		}
		if max == commits.None {
			if det != nil {
				det.Reason = "no member of the umbrella is released"
			}
			continue
		}
		ur, err := newRelease(cfg, uname, ust, max, channel)
		if err != nil {
			return nil, err
		}
		plan.Umbrellas = append(plan.Umbrellas, UmbrellaRelease{
			Name: uname, Current: ur.Current, Next: ur.Next, Tag: ur.Tag, Bump: ur.Bump, Channel: channel, Members: members,
		})
		if det != nil {
			det.Released, det.Next, det.Tag = true, ur.Next, ur.Tag
		}
	}
	return plan, nil
}

func maxBump(a, b commits.Bump) commits.Bump {
	if a > b {
		return a
	}
	return b
}

// noReleaseReason says in words why a component has no release of its own.
func noReleaseReason(name string, st tagState, considered, skipped int, onlyOld bool) string {
	since := "the beginning of history"
	if st.stableTag != "" {
		since = st.stableTag
	}
	switch {
	case considered == 0:
		return fmt.Sprintf("no commit since %s touches %s", since, name)
	case skipped == considered:
		return fmt.Sprintf("all %d relevant commit(s) since %s are marked [skip release]", considered, since)
	case onlyOld:
		return fmt.Sprintf("nothing new since the latest tag %s (earlier changes are already in it)", st.latestTag())
	default:
		return fmt.Sprintf("%d relevant commit(s) since %s, but none is significant (feat, fix, perf or a breaking change)", considered-skipped, since)
	}
}

func newRelease(cfg *config.Config, name string, st tagState, b commits.Bump, channel string) (*Release, error) {
	next, err := nextVersion(cfg, st, b, channel)
	if err != nil {
		return nil, err
	}
	rel := &Release{Component: name, Bump: b.String(), Channel: channel, Next: next.String(), Tag: cfg.TagName(name, next.String())}
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

// matchAny reports whether the file matches one of the glob patterns. A pattern that names a directory
// ("services/orders" or "services/orders/") also covers everything inside it.
func matchAny(patterns []string, file string) bool {
	for _, p := range patterns {
		if ok, _ := doublestar.Match(p, file); ok {
			return true
		}
		if ok, _ := doublestar.Match(strings.TrimSuffix(p, "/")+"/**", file); ok {
			return true
		}
	}
	return false
}

// ChangedOptions selects how "changed" is computed.
type ChangedOptions struct {
	Options
	// Base, when set, compares the current ref with the point where it diverged from Base
	// (typical for a merge request pipeline). Empty means: changes since each component's latest release.
	Base string
	// WithDependents also lists everything that (transitively) depends on a changed component.
	WithDependents bool
}

// Changed lists the components with changes, sorted. Unlike a release plan it does not care whether
// the changes are significant: any commit that touches the component's files (or names it in its scope)
// counts, because the point is to know what to build and test.
func Changed(cfg *config.Config, r git.Reader, opt ChangedOptions) ([]string, error) {
	set := map[string]bool{}
	if opt.Base != "" {
		files, err := r.ChangedFiles(opt.Base)
		if err != nil {
			return nil, err
		}
		for _, name := range cfg.Names() {
			if touches(cfg.Components[name], files) {
				set[name] = true
			}
		}
	} else {
		states := map[string]tagState{}
		var bases []string
		for _, name := range cfg.Names() {
			tags, stableTags, err := opt.tags(r, cfg.TagPrefix(name))
			if err != nil {
				return nil, err
			}
			states[name] = resolveState(tags, stableTags, opt.Channel)
			bases = append(bases, states[name].latestTag())
		}
		if hl, ok := r.(git.HistoryLoader); ok {
			if err := hl.LoadHistory(bases); err != nil {
				return nil, err
			}
		}
		for _, name := range cfg.Names() {
			cs, err := r.Commits(states[name].latestTag())
			if err != nil {
				return nil, err
			}
			for _, c := range cs {
				if Relevance(cfg, name, c, commits.Parse(c.Subject, c.Body)) != "" {
					set[name] = true
					break
				}
			}
		}
	}
	if opt.WithDependents {
		// walk the reversed dependency graph until nothing new is added
		for changed := true; changed; {
			changed = false
			for _, name := range cfg.Names() {
				if set[name] {
					continue
				}
				for _, d := range cfg.Components[name].DependsOn {
					if set[d] {
						set[name], changed = true, true
						break
					}
				}
			}
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}
