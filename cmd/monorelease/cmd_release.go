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

// cmdRelease creates the tags of the plan and then, as configured, writes notes files, pushes the
// tags and creates GitHub/GitLab releases.
func cmdRelease(cfg *config.Config, g *git.CLI, plan *planner.Plan, o options) error {
	// component tags first, then umbrella tags (their message holds the member versions)
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

	if !o.push {
		return nil
	}
	// a single atomic push: either all tags reach the remote or none
	if err := g.PushTags(o.remote, names); err != nil {
		return err
	}
	fmt.Printf("pushed %d tags to %s\n", len(names), o.remote)

	if o.noReleases {
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
		var noToken forge.ErrNoToken
		if explicit || !errors.As(err, &noToken) {
			return fmt.Errorf("tags are pushed, but creating releases failed: %w", err)
		}
		fmt.Fprintf(os.Stderr, "skipping %s releases: %v\n", noToken.Platform, err)
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
