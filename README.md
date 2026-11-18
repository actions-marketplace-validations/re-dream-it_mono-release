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
git clone https://github.com/OWNER/mono-release.git
cd mono-release
go build -o monorelease ./cmd/monorelease
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
| `branches[].name` | Release branch. May be a glob such as `rc/*`. |
| `branches[].prerelease` | Pre-release channel of the branch (`beta`, `rc`, ...). Empty or omitted means stable releases. |
| `components.<name>.paths` | Glob patterns of the files that belong to the component. A component may span several directories. Required. |
| `components.<name>.exclude` | Glob patterns of files that do not count as a change of the component. |
| `components.<name>.depends_on` | Components this one depends on. Releasing a dependency also releases this component. |
| `components.<name>.cascade` | Bump applied when a dependency is released: `patch` (default), `minor` or `none`. |
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
monorelease release                      create tags for the plan
monorelease version [name]               latest released version of a component or umbrella (or of all of them)
monorelease manifest <umbrella> [ver]    components and versions in an umbrella release
```

Flags:

| Flag | Meaning |
|---|---|
| `-c <file>` | config path (default `mono-release.yaml`) |
| `-C <dir>` | repository directory (default `.`) |
| `--branch <name>` | branch to work from (default: `$CI_COMMIT_BRANCH`, else the checked-out branch) |
| `--json` | JSON output (`plan`, `version`, `manifest`) |
| `--stable` | `version`: only stable versions, whatever the branch |
| `--component <name>` | `manifest`: print only that component's version |
| `--push` | `release`: push the created tags |
| `--remote <name>` | remote for `--push` (default `origin`) |

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

## Versioning rules

**Bump** is derived from [Conventional Commits](https://www.conventionalcommits.org/):

| Commit | Bump |
|---|---|
| `feat:` | minor |
| `fix:`, `perf:` | patch |
| `feat!:` or a `BREAKING CHANGE:` footer | major |
| anything else | none |

The highest bump among the relevant commits wins. A commit is relevant to a component when it changes a file matching its `paths` and not matching `exclude`.

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


## Adopting it in an existing repository

mono-release only looks at tags named `<component>@<version>`; other tags (for example semantic-release's `v1.2.3`) are ignored. In a repository with history and no such tags, every component with significant commits would be released as `initial_version`. To continue from your current versions, tag the commit that corresponds to each current version first:

```sh
git tag -a orders@2.7.0 -m baseline <commit-of-that-release>
git tag -a billing@3.1.2 -m baseline <commit-of-that-release>
git push origin --tags
```

Only commits after those tags are considered from then on.

## Limitations

- Versions live in git tags only. mono-release does not edit version fields in files such as `pom.xml` or `package.json` and does not create commits.
- It does not generate a changelog file yet. `monorelease plan --json` lists the commits behind each release.
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

Tests run against real temporary git repositories. Set `MONO_RELEASE_TRACE=1` to see the git calls the tool makes.

## License

[MIT](LICENSE)
