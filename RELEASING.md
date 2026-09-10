# Releasing Helm charts

NKTP and OCCT are versioned and released independently. NKTP is an application
chart; OCCT is a library chart used by other charts.

## Development builds

Pushes to `main` publish development builds of charts whose packaged files or
publishing configuration changed. Each version combines the chart's `Chart.yaml`
version with the first 8 characters of the commit SHA, for example
`0.1.0-dev.gdf415899`. Rerunning publication for the same commit uses the same
version. Existing prerelease versions receive the development suffix, and build
metadata is omitted. These builds let consumers try merged changes before a
release. Pull requests run validation without publishing charts.

## Releasing a change

Chart versions and release tags are currently managed manually. Automatic version
bumps based on Conventional Commits are planned as a follow-up.

1. Make the chart changes and update its `Chart.yaml` version. Include any
   dependency and lockfile updates required by the change.
2. Open a pull request, complete review and validation, and merge it into `main`.
3. Create and push the chart's release tag at the reviewed merge commit. The tag
   version must exactly match the version in that commit's `Chart.yaml`.
4. Confirm that the chart publishing run in GitHub Actions succeeds. A chart's
   release tag publishes only that chart.
5. Record the published version and artifact digest, and update consumers to
   the intended version.

| Chart | Version file | Example release tag |
| --- | --- | --- |
| NKTP | `nktp/Chart.yaml` | `nktp/v0.2.0` |
| OCCT | `occt/chart/Chart.yaml` | `occt/v0.2.0` |

For a release candidate, use a prerelease version such as `0.2.0-rc.1` and the
matching tag, such as `nktp/v0.2.0-rc.1`. For the stable release, update the chart
version to `0.2.0` through review and create a new `nktp/v0.2.0` tag.

Treat release tags and published versions as immutable. Do not move a release
tag or reuse a published version for changed chart content. A subsequent change
requires a new version and tag.

## What publication validates

The publishing job builds NKTP's locked dependencies, packages the selected
chart and lints the archive. It also renders NKTP's templates. OCCT is linted
without a standalone render because it is a library chart. Chart names and
container image references are preserved; a chart release does not publish
container images.

The run logs include the package checksum and published artifact digest. The
run summary identifies the chart version and source commit.

Maintainers can also start a chart publishing run manually from `main` or a
matching chart release tag. A manual run from `main` uses the development version
for the selected commit; a run from a release tag uses that tag's version. Other
branches and the other chart's release tags do not publish through that entrypoint.

> **Note:** Currently, GitHub Actions are authenticated with private NVIDIA
> registries using secure credentials.
