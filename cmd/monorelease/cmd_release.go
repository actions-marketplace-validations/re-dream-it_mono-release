package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/re-dream-it/mono-release/internal/config"
	"github.com/re-dream-it/mono-release/internal/forge"
	"github.com/re-dream-it/mono-release/internal/git"
	"github.com/re-dream-it/mono-release/internal/planner"
)

// cmdRelease creates the tags of the plan and then, as configured, pushes them, writes notes files and
// creates GitHub/GitLab releases. If the tags cannot be created or pushed the local tags are removed
// again: a tag that exists only locally would make the next run believe the version was released.
func cmdRelease(cfg *config.Config, g *git.CLI, plan *planner.Plan, o options) error {
	// component tags first, then umbrella tags (their message holds the member versions)
	var names []string
	rollback := func(err error) error {
		if derr := g.DeleteTags(names); derr != nil {
			return fmt.Errorf("%w (and the local tags %s could not be removed: %v)", err, strings.Join(names, ", "), derr)
		}
		return fmt.Errorf("%w (the local tags were removed again, so a retry plans the same release)", err)
	}
	for _, r := range plan.Releases {
		if err := g.CreateTag(r.Tag, plan.SHA, ""); err != nil {
			return rollback(err)
		}
		fmt.Println("tagged", r.Tag)
		names = append(names, r.Tag)
	}
	for _, u := range plan.Umbrellas {
		if err := g.CreateTag(u.Tag, plan.SHA, u.Manifest()); err != nil {
			return rollback(err)
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
			return rollback(err)
		}
		fmt.Printf("pushed %d tags to %s\n", len(names), o.remote)
	}

	// notes files are written into the working tree, never committed: publish them as CI artifacts
	if len(cfg.Notes) > 0 && !o.noNotes {
		files, err := newGenerator(cfg, g, o.remote).Write(o.dir, "")
		if err != nil {
			return fmt.Errorf("tags are created, but writing notes failed: %w", err)
		}
		for _, f := range files {
			fmt.Println("wrote", f)
		}
	}

	if !o.push || o.noReleases {
		return nil
	}
	umbrellas, components := cfg.ReleaseTargets()
	if !umbrellas && !components {
		return nil
	}
	return createReleases(cfg, g, plan, o, umbrellas, components)
}

// createReleases creates a GitHub/GitLab release for the tags just pushed. When "releases" is not
// set in the config, a missing token only produces a note; when it is set, it is an error.
func createReleases(cfg *config.Config, g *git.CLI, plan *planner.Plan, o options, umbrellas, components bool) error {
	explicit := cfg.Releases != ""
	webURL := cfg.RepositoryURL
	if webURL == "" {
		webURL = newGenerator(cfg, g, o.remote).RepoURL
	}
	client, err := forge.FromEnv(webURL)
	if err != nil {
		if explicit {
			return fmt.Errorf("tags are pushed, but creating releases failed: %w", err)
		}
		// the default setting must never break a release: a remote that is not GitHub or GitLab, or a
		// missing token, only means there is no release page to create
		var noToken forge.ErrNoToken
		if errors.As(err, &noToken) {
			fmt.Fprintf(os.Stderr, "skipping %s releases: %v\n", noToken.Platform, err)
		} else {
			fmt.Fprintf(os.Stderr, "skipping GitHub/GitLab releases: %v\n", err)
		}
		return nil
	}

	gen := newGenerator(cfg, g, o.remote)
	type target struct{ name, version, tag string }
	var targets []target
	if components {
		for _, r := range plan.Releases {
			targets = append(targets, target{r.Component, r.Next, r.Tag})
		}
	}
	if umbrellas {
		for _, u := range plan.Umbrellas {
			targets = append(targets, target{u.Name, u.Next, u.Tag})
		}
	}
	for _, t := range targets {
		body, err := gen.Notes(t.name, t.version)
		if err != nil {
			return fmt.Errorf("notes for %s: %w", t.tag, err)
		}
		pre := strings.Contains(t.version, "-")
		created, err := client.CreateRelease(t.tag, t.name+" "+t.version, body, pre)
		if err != nil {
			return err
		}
		if created {
			fmt.Printf("created %s release for %s\n", client.Name(), t.tag)
		} else {
			fmt.Printf("%s release for %s already exists\n", client.Name(), t.tag)
		}
	}
	return nil
}
