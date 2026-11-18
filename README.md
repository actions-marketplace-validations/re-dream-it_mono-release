# mono-release

Automatic versioning and release tagging for **monorepos**. Every component (a service, a library) gets its own version, its own tags and its own rules for what counts as a change, and sets of components can be versioned together as an **umbrella**. It works just as well in a single-package repository: that is simply one component.



> **Status:** early development. The CLI is usable; a server and web UI are planned (see [Roadmap](#roadmap)).


mono-release gives you:

- **Independent versions per component**, tagged `orders@1.4.0`.
- **Flexible change detection**: each component lists the paths that belong to it (it may be spread over several directories) and paths to ignore.
- **Dependency cascade**: releasing a shared library bumps the components that depend on it, with a configurable bump per component.
- **Umbrella releases**: a versioned manifest of component versions that belong together, so whoever deploys knows which tags to use.
- **Pre-release channels** per branch (`1.5.0-beta.1`), independent per component.
- **Tags are the source of truth.** No state files, no release commits, nothing written back to your repository.
- **Fast on large repositories**: the number of git calls does not depend on the number of components.
- **Release notes** per component or umbrella, generated from the commits and written to files or published as GitHub/GitLab releases.
- **CI helpers**: `changed` lists what to build, `explain` says why a version was chosen, `--ref` reads any branch without a checkout.
- **Escape hatches**: Conventional Commit scopes as an extra signal, `[skip release]` and `--force`.

## How it works

1. Each component has a config entry with `paths`.
2. For every component, mono-release finds its latest version tag (`<component>@<version>`), looks at the commits since that tag that touched the component's files, and derives the bump from [Conventional Commits](https://www.conventionalcommits.org/).
3. Released components trigger a cascade through the dependency graph.
4. An umbrella is released when any of its members is, and records the versions of all its members.
5. `monorelease release` creates the tags (and optionally pushes them). `monorelease plan` only shows what would happen.

## Requirements

- `git` 2.31 or newer
- Go 1.27+ to build from source

## Installation

Build from source:

```sh
git clone https://github.com/re-dream-it/mono-release.git
cd mono-release
go build -o monorelease ./cmd/monorelease
```

Or install the latest commit with the Go toolchain (the binary then reports `dev` as its version; use a release build for a versioned one):

```sh
go install github.com/re-dream-it/mono-release/cmd/monorelease@latest
```

This produces a single static binary. Copy it somewhere on your `PATH`. For a Linux CI runner cross-compile with `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o monorelease ./cmd/monorelease`.

Or go to release page and download ready binary for your system

## Quick start

Copy [examples/mono-release.yaml](examples/mono-release.yaml) to the repository root as `mono-release.yaml` and adjust the component names and paths to your layout. The example is annotated and covers everything described below. A working configuration needs nothing more than a `components` map where every component has `paths`.

Then:

```sh
monorelease plan             # show what would be released, change nothing
monorelease release          # create the tags locally
monorelease release --push   # create and push them
```

Example output of `plan` with the example configuration, after a `feat:` commit in `libs/common`:

```
billing: 1.1.0 -> 1.2.0 [minor, 0 commits, cascade from common-lib]
common-lib: 1.0.1 -> 1.1.0 [minor, 1 commits]
orders: 1.1.0 -> 1.1.1 [patch, 0 commits, cascade from common-lib]
umbrella platform: 1.4.0 -> 1.5.0 [minor]
  billing@1.2.0
  common-lib@1.1.0
  notifications@1.0.0
  orders@1.1.1
```

`billing` and `orders` are released because they depend on `common-lib`, each with the bump from its own `cascade` setting. `notifications` has `cascade: none`, so it keeps its version, which is recorded in the `platform` umbrella.

## Configuration

The configuration lives in `mono-release.yaml` in the repository root (override with `-c`). A complete, annotated example is in [examples/mono-release.yaml](examples/mono-release.yaml).

| Key | Description |
|---|---|
| `initial_version` | Version of a component's first release. Default `1.0.0`. |
| `releases` | For which tags a GitHub/GitLab release is created on `release --push`: `umbrella`, `components`, `all` or `none`. Default: `umbrella` if the config has umbrellas, otherwise nothing. |
| `repository_url` | Web URL of the repository for links in the notes, for example `https://github.com/acme/mono`. Derived from the remote when omitted. |
| `notes[].for` | Component or umbrella the notes are generated for, or `*` for all of them. |
| `notes[].file` | Output file, relative to the repository. `{name}` and `{version}` are replaced. |
| `notes[].history` | `latest` (default): notes of the newest release. `all`: a changelog of every release. |
| `branches[].name` | Release branch. May be a glob such as `rc/*`. |
| `branches[].prerelease` | Pre-release channel of the branch (`beta`, `rc`, ...). Empty or omitted means stable releases. |
| `components.<name>.paths` | Glob patterns of the files that belong to the component. A component may span several directories. Required. |
| `components.<name>.exclude` | Glob patterns of files that do not count as a change of the component. |
| `components.<name>.depends_on` | Components this one depends on. Releasing a dependency also releases this component. |
| `components.<name>.cascade` | Bump applied when a dependency is released: `patch` (default), `minor` or `none`. |
| `components.<name>.scopes` | Extra Conventional Commit scopes that point at this component, besides its own name. |
| `umbrellas.<name>.members` | Components that belong to the umbrella. |
| `umbrellas.<name>.all` | `true` makes the umbrella cover every component, including ones added later. Cannot be combined with `members`. |

Rules:

- Component and umbrella names must not contain `@`, `/` or whitespace, and must not clash with each other.
- `paths` and `exclude` are glob patterns (`**` is supported).
- Unknown fields, unknown dependencies, dependency cycles and an umbrella with both `all` and `members` are configuration errors.
- If `branches` is set and the current branch is not listed, commands stop with an error instead of releasing from an unexpected branch.

## Commands

```
monorelease plan                         show what would be released and the next versions
monorelease release                      create tags for the plan (--push also pushes them and creates releases)
monorelease version [name]               latest released version of a component or umbrella (or of all of them)
monorelease manifest <umbrella> [ver]    components and versions in an umbrella release
monorelease changed                      components with changes (what to build and test)
monorelease explain <name>               why a component or umbrella is, or is not, released
monorelease notes [name] [ver]           release notes; without a name writes the files configured in `notes`
monorelease completion <shell>           shell completion script (bash, zsh, fish)
```

Flags:

| Flag | Meaning |
|---|---|
| `-v`, `--version` | print the version of monorelease itself and exit |
| `-c <file>` | config path (default `mono-release.yaml`) |
| `-C <dir>` | repository directory (default `.`) |
| `--branch <name>` | branch to work from (default: `$CI_COMMIT_BRANCH`, else the checked-out branch) |
| `--ref <ref>` | read this branch, tag or commit instead of the checked-out one (not for `release`) |
| `--json` | JSON output |
| `--stable` | `version`: only stable versions, whatever the branch |
| `--component <name>` | `manifest`: print only that component's version |
| `--force <name>[=bump]` | `plan`/`release`: release the component even without significant commits; bump is `patch` (default), `minor` or `major`; may be repeated |
| `--base <ref>` | `changed`: compare with the point where the current ref diverged from this ref |
| `--with-dependents` | `changed`: also list the components that depend on changed ones |
| `--write` | `notes <name>`: write the configured files for that name instead of printing |
| `--all` | `notes <name>`: print the changelog of every release |
| `--push` | `release`: push the created tags |
| `--remote <name>` | remote for `--push` and for commit links (default `origin`) |
| `--no-notes` | `release`: do not write the configured notes files |
| `--no-releases` | `release`: do not create GitHub/GitLab releases |

Flags may be placed before or after positional arguments.

### Reading versions

```sh
monorelease version                      # all components and umbrellas
monorelease version orders               # just the version: 1.4.0 (script friendly)
monorelease version platform             # an umbrella's version
monorelease version --json
monorelease manifest platform            # members of the latest platform release, one component@version per line
monorelease manifest platform 1.2.0      # members of a specific umbrella release
monorelease manifest platform --component orders   # orders' version in the latest platform release
```

On a pre-release branch the pre-release versions are shown (`1.5.0-beta.2`), on a stable branch only stable ones. `--stable` always gives stable versions. A component without releases is printed as `(none)` in the list and makes `version <name>` exit with status 1. The manifest is stored in the message of the umbrella's tag, so `manifest` works for tags created by `monorelease release`.

`release --push` pushes all new tags in a single atomic push.

### What changed (for CI)

```sh
monorelease changed                         # components with changes since their last release
monorelease changed --base origin/main      # merge request pipeline: what this branch changes
monorelease changed --base origin/main --with-dependents --json
```

Prints the names of the components with changes, one per line, so a pipeline can build and test only those. With `--base` the changes are those made on the current branch since it diverged from the base (a three-dot diff). Without it, they are the commits since each component's latest release. Any commit counts, significant or not, because the point is what to build. `--with-dependents` adds everything that depends on a changed component, transitively. Unlike `plan`, `changed` works on any branch, including a merge request's feature branch.

### Why this version? (explain)

```
$ monorelease explain orders
orders (component)
  branch:   stable
  current:  1.1.0 (tag orders@1.1.0)
  counting commits since: orders@1.1.0

  commits that count for this component:
    6ae3582  fix: typo [skip release]                     via paths  ignored ([skip release])
    2047553  fix: null pointer in checkout                via paths  patch
    01fdd98  feat(api): new gateway route                 via scope  minor

  highest bump from commits: minor
  cascade: common-lib released, so this component gets at least patch

  result: release 1.1.0 -> 1.2.0 (tag orders@1.2.0)
```

For a component that is not released it says why instead (no commits touch it, none is significant, all are skipped, a dependency released but `cascade` is `none`, ...). `--json` gives the same data for tools.

### Reading another ref

`--ref` makes every read command behave as if that revision were checked out, without touching your working tree:

```sh
monorelease version --ref origin/beta
monorelease explain orders --ref origin/main
```

The branch decides the release channel, so for a branch name it is taken from the ref (`origin/beta` means `beta`). For a tag or a commit say which branch it belongs to with `--branch`. `release` always works on the checked-out commit and refuses `--ref`.

### Release notes

Notes are generated from tags and commits, so they can be produced for any existing release:

```sh
monorelease notes orders 1.2.0      # print the notes of one release (default: the latest)
monorelease notes platform          # an umbrella
monorelease notes platform --all    # a changelog of every release
monorelease notes                   # write the files configured in the `notes` section
```

The notes of a **component** list its commits since the previous release, grouped into breaking changes, features, bug fixes and performance, with the rest folded into a collapsible "Other changes" block. Commits marked `[skip release]` and merge commits are left out, and every commit links to the repository. The notes of an **umbrella** show a table of its members' versions (what changed since the previous umbrella release is marked `old → new`) followed by the notes of every member that changed. A component that was released only because a dependency changed says so.

Which notes are written, and where, is configured in the `notes` section (see [examples/mono-release.yaml](examples/mono-release.yaml)). `monorelease release` writes those files after tagging. They are written into the working tree and never committed; in CI keep them as job artifacts. Commit links are built from the `origin` URL (credentials are stripped) or from `repository_url`.

### GitHub and GitLab releases

With `release --push`, mono-release can create a release page for each new tag, with the notes as its text. The `releases` setting says for which tags: `umbrella` (the default when the config has umbrellas), `components`, `all` or `none`. Without umbrellas nothing is created until you set it.

- The platform is detected from the remote URL. For other hosts set `MONO_RELEASE_FORGE=github` or `gitlab`.
- GitHub needs `GITHUB_TOKEN` (or `GH_TOKEN`). GitLab needs `GITLAB_TOKEN`, or uses `CI_JOB_TOKEN` inside GitLab CI.
- Pre-release versions are marked as pre-releases on GitHub.
- An already existing release is skipped, so a re-run is harmless.
- If `releases` is not set and no token is found, a note is printed and the run continues. If `releases` is set explicitly, a missing token is an error.

### Shell completion

```sh
source <(monorelease completion bash)         # or add it to ~/.bashrc
monorelease completion zsh > "${fpath[1]}/_monorelease"
monorelease completion fish | source
```

Commands, flags and the component and umbrella names from your configuration are completed.

## Versioning rules

**Bump** is derived from [Conventional Commits](https://www.conventionalcommits.org/):

| Commit | Bump |
|---|---|
| `feat:` | minor |
| `fix:`, `perf:` | patch |
| `feat!:` or a `BREAKING CHANGE:` footer | major |
| anything else | none |

The highest bump among the relevant commits wins. A commit is relevant to a component when it changes a file matching its `paths` and not matching `exclude`, **or** when its scope names the component.

**Scopes.** `feat(orders): ...` counts for the component `orders` even if the commit changed nothing under its `paths`. A scope may list several components (`feat(orders, billing): ...`), and a component can have aliases in its `scopes` setting, so `feat(api): ...` can point at `orders`. A scope never removes a commit from another component: a commit that touches the paths of a component still counts for it whatever its scope says.

**Skipping.** A commit with `[skip release]` (or `[release skip]`) in its message is ignored completely: it never causes a release or raises a bump, whatever its type, and it does not appear in the notes.

**Forcing.** `monorelease release --force orders` releases `orders` even though it has no significant commits, with a patch bump; `--force orders=minor` chooses the bump. A forced bump never lowers what the commits already require, and the cascade and umbrellas follow from a forced release as from any other.

**First release.** A component with no tags is released as `initial_version`, whatever the bump. A component without any significant commit is not released.

**Cascade.** When a component is released, every component that depends on it is released too, with the bump from its `cascade` setting (never lower than what its own commits require). Components that have never been released are not part of a cascade.

**Umbrella.** Released when any member is released; its bump is the highest bump among the released members. The member versions (new versions for released members, current versions for the rest) are recorded in the tag message.

**Pre-releases.** On a branch with `prerelease: <channel>`:

- The target version is the bump from the last *stable* version over all accumulated commits (`1.4.0` + a `feat` = `1.5.0`), followed by a channel counter: `1.5.0-beta.1`, `1.5.0-beta.2`, ...
- A release is made only if there are new significant commits since the latest tag, including pre-release tags.
- The version line never moves backwards, and the counter restarts when stable catches up.
- Which stable version is "latest" is determined from the **stable branches** in the config (the local branch and `origin/<name>`), not just the history of the current branch. You do not need to merge the stable branch back into a pre-release branch for it to notice a new stable release.
- Pre-release tags of other channels are ignored; on a stable branch all pre-release tags are ignored.

**Merge strategies.** Fast-forward, squash and merge commits all work. Commits brought in by a merge count as ordinary commits, so a merge commit with a non-conventional title (such as GitLab's default `Merge branch 'x' into 'main'`) is harmless when the commits inside are conventional. A merge commit is also evaluated by what it brought into the branch, which covers the case where only the merge title is conventional.

## Using it in CI

An example GitLab CI job is in [examples/gitlab-ci.yml](examples/gitlab-ci.yml). Things to get right:

- Full history: `GIT_DEPTH: "0"`.
- Pre-release branches need to see the stable branch: `git fetch origin main --tags`.
- A token that can push tags (`write_repository`) in the job's `origin` URL.
- Run `monorelease plan` before `monorelease release --push` so the decision is visible in the log.
- Keep the notes files as artifacts (`artifacts: paths: [out/]` in the example).

**Docker.** The [Dockerfile](Dockerfile) builds an Alpine image with `monorelease` and git (about 45 MB): `docker build -t monorelease .`. In GitLab CI use `image: { name: ..., entrypoint: [""] }` so the job script can run.

**GitHub Actions.** The repository is also a Docker action ([action.yml](action.yml)); an example workflow is in [examples/github-actions.yml](examples/github-actions.yml). It needs `fetch-depth: 0` and passes `GITHUB_TOKEN` for the releases. The action is a first sketch: it builds the image on every run, and there is no published action version yet.


## Adopting it in an existing repository

mono-release only looks at tags named `<component>@<version>` (for example `orders@2.7.0`); the format is fixed, and other tags (for example semantic-release's `v1.2.3`) are ignored. In a repository with history and no such tags, every component with significant commits would be released as `initial_version`. To continue from your current versions, tag the commit that corresponds to each current version first:

```sh
git tag -a orders@2.7.0 -m baseline <commit-of-that-release>
git tag -a billing@3.1.2 -m baseline <commit-of-that-release>
git push origin --tags
```

Only commits after those tags are considered from then on.

## Limitations

- Versions live in git tags only. mono-release does not edit version fields in files such as `pom.xml` or `package.json` and does not create commits.
- Notes and changelogs are generated into files in the working tree; they are not committed back to the repository.
- Release branches for maintaining old versions (such as `release/1.x`) are not supported yet.
- GitLab CI is the reference environment. The tool itself only needs git, so other CI systems work too, but there are no ready-made integrations.

## Roadmap

- A server with a database and a web UI to browse versions, changelogs and umbrella releases.


- Maintenance branches.

## Development

```sh
go build ./...
go test ./...
```

Tests run against real temporary git repositories. Set `MONO_RELEASE_TRACE=1` to see the git calls the tool makes. `make build` builds `./monorelease` with the version from the nearest tag; `make test` runs vet and the tests.

## Releasing

mono-release versions itself with mono-release: the configuration is [mono-release.yaml](mono-release.yaml), the tags are `monorelease@1.2.3`, and the history of every release is in [CHANGELOG.md](CHANGELOG.md). The next version follows from the commit messages, so write them as [Conventional Commits](https://www.conventionalcommits.org/): `feat:` for a new feature, `fix:` for a bug fix, `feat!:` for a breaking change. Commits that only change documentation, tests or this repository's tooling do not cause a release.

```sh
scripts/release.sh          # or: make release
```

The script runs the tests, shows the plan, and after confirmation pushes the commits, tags the version, regenerates `CHANGELOG.md` and commits it (marked `[skip release]`, so it does not trigger another release), and creates the GitHub release. It reads a GitHub token from `GITHUB_TOKEN` or `gh auth token`. `monorelease --version` shows the version of a build: the release version for release builds, and `0.1.1-3-g1a2b3c4` (three commits after `v0.1.1`) for a checkout.

### Release artifacts

`scripts/release.sh` also builds the files of the release and attaches them to its GitHub release, so one command does the whole release. To run that step on its own (for a release that is already tagged):

```sh
scripts/build-release.sh --upload         # or: make dist, which only builds
```

It builds from the release tag, not from the working tree, so the artifacts are exactly what was released, and writes to `dist/`:

- `monorelease_<version>_<os>_<arch>.tar.gz` for linux and darwin, `.zip` for windows, each for amd64 and arm64, with the license and README;
- `checksums.txt` with the SHA-256 of every archive;
- `RELEASE_NOTES.md`, the notes of the release as text for the release page;
- the Docker image `monorelease:<version>` (and `:latest` for a stable version), built for this machine's platform, unless `--no-docker` is given.

With `--upload` the GitHub release is created from `RELEASE_NOTES.md` if it does not exist yet (a version with a pre-release part such as `-beta.1` is marked as a pre-release), and the archives and checksums are attached; if it exists, the files are uploaded over the old ones. This needs the GitHub CLI logged in (`gh auth login`) or `GITHUB_TOKEN`. Without it `scripts/release.sh` still tags and pushes, builds the files, and prints a warning instead of uploading.

**Docker image.** `scripts/release.sh` also pushes a multi-architecture image (`linux/amd64` and `linux/arm64`, tagged with the version and, for a stable version, `latest`) to `ghcr.io/<owner>/monorelease`. It logs in to `ghcr.io` with the GitHub CLI token, which needs the `write:packages` scope. Add it once:

```sh
gh auth refresh -h github.com -s write:packages
```

Without the scope the image is skipped with a warning and the rest of the release goes on. A package that is published for the first time is private by default; make it public once in the package settings on GitHub if the image should be pullable by everyone. Use `--image NAME` to push to another registry (after `docker login`) and `--no-image` to skip the image.

Other options: `scripts/release.sh --no-artifacts` to only tag and push; `scripts/build-release.sh --version 0.2.0` for an older release, `--push NAME` to publish the image of a release that is already tagged, and `--snapshot` to try the build on the working tree. The scripts need Go, `git`, `tar`, `zip` and, for the image, Docker.

## License

[MIT](LICENSE)
