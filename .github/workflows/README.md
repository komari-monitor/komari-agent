# Workflow Design Notes

This directory contains the GitHub Actions workflows for Komari Agent. Keep this
document updated when changing release, snapshot, or Docker publishing behavior.

The important design split is:

- Stable releases use normal GitHub releases and semver tags.
- Snapshot builds use GitHub prereleases named `Snapshot-yymmddhhMM`.
- The Docker snapshot image intentionally uses the mutable tag `snapshot`.
- Stable auto-update must not consume snapshot prereleases.

## Workflow Summary

| Workflow | Trigger | Main output | Prerelease handling |
| --- | --- | --- | --- |
| `build.yml` | Push to `main` | CI build artifacts for the pushed commit | Not a release workflow |
| `snapshot.yml` | Push to `main`, manual dispatch | One snapshot prerelease plus `ghcr.io/...:snapshot` | Creates prereleases only |
| `release.yml` | Published GitHub release | Release binary assets | Skips prereleases |
| `release-docker.yml` | Published GitHub release, manual dispatch | Stable Docker image tags | Skips prerelease release events |
| `generate-release-notes.yml` | Published GitHub release, manual dispatch | Generated release notes | Skips prerelease release events |

## Common Build Conventions

Binary names must remain compatible with the updater, installer scripts, and
Dockerfile:

- Release and snapshot assets are named `komari-agent-${GOOS}-${GOARCH}`.
- Windows assets append `.exe`.
- The Dockerfile expects prebuilt Linux binaries named
  `komari-agent-${TARGETOS}-${TARGETARCH}` in the Docker build context.

The agent version is embedded with:

```sh
-ldflags="-X github.com/komari-monitor/komari-agent/update.CurrentVersion=${VERSION}"
```

Do not remove this without changing the agent update and reporting logic. The
agent uses `update.CurrentVersion` for update checks and reports it as part of
basic info.

Prefer `go-version-file: go.mod` for release-producing workflows so Actions uses
the Go version declared by the project.

## `build.yml`

Purpose: quick build validation on `main`.

Trigger:

- Runs on every push to `main`.
- A single `git push` containing multiple commits creates one workflow run for
  the pushed ref tip. Multiple separate pushes can create multiple runs.

Jobs:

- Builds a matrix of Windows, Linux, macOS, and FreeBSD targets.
- Excludes unsupported combinations:
  - `windows/arm`
  - `darwin/386`
  - `darwin/arm`
- Uploads build artifacts to the workflow run.

This workflow does not publish GitHub releases or Docker images.

## `snapshot.yml`

Purpose: publish the latest development build from `main`.

Trigger:

- Runs on push to `main`.
- Can be run manually with `workflow_dispatch`.

Race protection:

- Uses concurrency group `snapshot-${{ github.ref }}` with
  `cancel-in-progress: true`.
- Validates that the workflow is running for `refs/heads/main`.
- Validates that `GITHUB_SHA` is still the current `origin/main` commit before
  building.
- Re-checks current `origin/main` before publishing the prerelease.
- Re-checks current `origin/main` before publishing the Docker image.

This means that several separate pushes in a row may start several runs, but the
latest run is the one intended to publish. If an older run has already published,
the newer run deletes older snapshot prereleases after it publishes.

Snapshot version format:

```text
Snapshot-yymmddhhMM
```

The timestamp is UTC. The generated value is embedded into the binaries as
`update.CurrentVersion`.

Binary release job:

- Builds the same OS/architecture matrix as the normal release workflow.
- Uploads all binaries as workflow artifacts.
- Creates a GitHub release with `--prerelease`.
- Uploads all `komari-agent-*` artifacts to that prerelease.

Snapshot retention:

- After creating the current snapshot prerelease, the workflow lists prereleases
  whose tag starts with `Snapshot-`.
- It deletes every matching old snapshot release and its tag.
- The intended release state is exactly one snapshot prerelease: the latest one.

Docker job:

- Builds Linux `amd64` and `arm64` binaries for the Docker build context.
- Builds and pushes a multi-arch image.
- Publishes only this tag:

```text
ghcr.io/<owner>/<repo>:snapshot
```

### Why Docker Uses Only `:snapshot`

The mutable `snapshot` Docker tag is intentional.

Users who run prerelease containers usually want to follow the newest snapshot.
A stable tag reference such as `ghcr.io/...:snapshot` lets Docker image updaters
pull the same tag and detect that the image digest changed.

Adding immutable timestamp tags like `Snapshot-yymmddhhMM` can be useful for
traceability, but users pinned to such a timestamp tag will not automatically
move to the next snapshot. If timestamp image tags are added later, keep
`:snapshot` as the moving tag.

### Snapshot Docker Updates

Container-based updates and binary self-updates are different mechanisms:

- Watchtower-style tools update containers by comparing the image behind the
  configured tag. They should work with the mutable `:snapshot` tag because each
  new snapshot push changes the image digest.
- The agent's own self-update logic updates the binary inside the running
  container filesystem. That does not update the Docker image. If the container
  is recreated, the image contents win again.
- The Dockerfile creates `/.komari-agent-container`. Future update logic can use
  that marker to avoid binary self-update in containers or to log that image
  refresh should be handled outside the agent.

For Docker prerelease users, prefer `:snapshot` plus a container image updater.
For non-container prerelease users, implement snapshot-aware binary self-update.

## `release.yml`

Purpose: attach stable release binaries to a published GitHub release.

Trigger:

- Runs when a GitHub release is published.

Prerelease guard:

```yaml
if: ${{ !github.event.release.prerelease }}
```

This guard is important. Snapshot releases are GitHub prereleases, and this
workflow must not attach stable-release assets or perform stable-release behavior
for a snapshot.

Jobs:

- Builds Windows, Linux, macOS, and FreeBSD binaries.
- Embeds the release tag as `update.CurrentVersion`.
- Uploads the matching binary to the GitHub release.

## `release-docker.yml`

Purpose: publish stable Docker images.

Trigger:

- Runs when a GitHub release is published.
- Can be run manually with `workflow_dispatch`.

Prerelease guard:

```yaml
if: ${{ github.event_name == 'workflow_dispatch' || !github.event.release.prerelease }}
```

This means prerelease release events do not publish stable Docker tags. Manual
runs are still allowed.

Jobs:

- Builds Linux `amd64` and `arm64` binaries for the Docker build context.
- Builds and pushes a multi-arch image.
- On release events, publishes:
  - the release tag, such as `v1.2.3`
  - `latest`

Do not let snapshot prereleases publish `latest`.

## `generate-release-notes.yml`

Purpose: generate and apply GitHub release notes.

Trigger:

- Runs when a GitHub release is published.
- Can be run manually for a specific tag.

Prerelease guard:

```yaml
if: ${{ github.event_name == 'workflow_dispatch' || !github.event.release.prerelease }}
```

Snapshot prereleases intentionally keep their simple automated snapshot notes and
do not trigger normal stable-release note generation.

## Auto-Update Interaction

Current stable auto-update behavior:

- The agent calls `update.CheckAndUpdate()` when auto-update is enabled.
- `CheckAndUpdate()` uses `github.com/rhysd/go-github-selfupdate/selfupdate`.
- That library's normal latest-release detection skips GitHub prereleases.
- Therefore stable agents with auto-update enabled should not update to
  `Snapshot-*` prereleases.

Snapshot releases add a second layer of protection:

- They are marked as GitHub prereleases.
- Their tag format, `Snapshot-yymmddhhMM`, is not semver.

If future code intentionally supports prerelease/snapshot auto-update, do not use
the Docker image tag as the binary version source. The Docker tag is always
`snapshot` by design. Use the embedded binary version
`update.CurrentVersion == Snapshot-yymmddhhMM` or release metadata instead.

Container guidance for future update changes:

- A binary running in Docker can potentially replace `/app/komari-agent`, but
  that only changes the container's writable layer.
- After self-update, the current code exits with status `42`; the container needs
  a restart policy or external supervisor to come back.
- Recreating the container from the image discards any in-container binary
  replacement.
- Prefer image-level updates for Docker deployments.

## Change Checklist

Before changing these workflows, check:

- Snapshot releases are still created with `--prerelease`.
- Stable release workflows still skip prereleases.
- Snapshot cleanup still deletes old `Snapshot-*` prereleases after the new one
  is created.
- Docker snapshot publishing still keeps the mutable `snapshot` tag.
- Stable Docker publishing does not run for snapshot prereleases and does not
  publish `latest` for snapshots.
- Binary asset names still match updater, installer, and Dockerfile expectations.
- `update.CurrentVersion` is still embedded in all release-producing binaries.
- Race protection still prevents stale `main` commits from publishing snapshots.
