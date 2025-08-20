# Notes on Releases

## Cycles

Since the AI space moves quickly, Envoy AI Gateway targets a regular monthly release cadence (at least one release per calendar month) cut from the main branch, depending on the feature set and stability of the main branch at the time of the release.

Envoy AI Gateway is built on top of Envoy Gateway, so there is a notion of a "supported version" of Envoy Gateway that is compatible with Envoy AI Gateway.
Usually, the main branch of Envoy AI Gateway is tested against both the latest stable version of Envoy Gateway and its main branch.
However, Envoy AI Gateway may occasionally pick up critical new capabilities that are only available in the main branch of Envoy Gateway.
In such cases, if a monthly release is based on an Envoy Gateway main commit (and not an upstream stable tagged release), the release notes will contain a clear WARNING stating this dependency.
Envoy Gateway itself follows a roughly three month release cycle, so a "stable" Envoy AI Gateway release (i.e. one that is based on a tagged stable Envoy Gateway release) will be produced at least once every three months, aligned after (or shortly following) the corresponding Envoy Gateway stable release.

## Versioning

We increment the major version number when we have a major architectural change or a major feature addition.

Especially when we have a first stable control plane API, we will cut the major v1.0.0 release. Until then, we will use the version number v0.3.x, v0.4.y, etc. See the [support policy](#Support-Policy) for more details.

The patch version will be incremented when we have a bug fix or a security fix. The end of life for the version will be 2 releases after the release of the version. For example, if we release the version v0.1.0, the end of life for the version will be when we release the version v0.3.0.

## Release Artifacts

We have two kinds of release artifacts for each version of Envoy AI Gateway:
* Docker images for the controller as well as the extproc images.
* Helm charts for deploying the controller in Kubernetes.

Both artifacts are tagged with a release tag and published in the Docker Hub. For example, we have
* `docker.io/envoyproxy/ai-gateway-extproc:v0.2.1`
* `docker.io/envoyproxy/ai-gateway-controller:v0.2.1`
* `docker.io/envoyproxy/ai-gateway-helm:v0.2.1`
* `docker.io/envoyproxy/ai-gateway-crds-helm:v0.2.1`

published in the Docker Hub.

The main branch version of Envoy AI Gateway is also tagged just like the tagged released versions in the Docker Hub.
More precisely, the main version's helm chart is tagged with `v0.0.0-${commit hash of the main branch}`.

* `docker.io/envoyproxy/ai-gateway-extproc:${git_commit}`
* `docker.io/envoyproxy/ai-gateway-controller:${git_commit}`
* `docker.io/envoyproxy/ai-gateway-helm:v0.0.0-${git_commit}`
* `docker.io/envoyproxy/ai-gateway-crds-helm:v0.0.0-${git_commit}`

## Support Policy

This document focuses on compatibility concerns of those using Envoy AI Gateway.
It is important to note that the support policy is subject to change at any time. The support policy is as follows:

First, there are three areas of compatibility that we are concerned with:
* [Deploying the Envoy AI Gateway controller through the Kubernetes Custom Resource Definition (CRD)](#Custom-Resource-Definitions).
* [Upgrading the Envoy AI Gateway controller](#Upgrading-the-Envoy-AI-Gateway-controller).
* [Envoy Gateway vs Envoy AI Gateway compatibility](#Envoy-Gateway-vs-Envoy-AI-Gateway-compatibility).

Note: Since we do not envision users will consume this project as a library, except for CRD api/*/*.go files, we do not guarantee any compatibility for the Go public APIs exposed in this project.

### Custom Resource Definitions

The Custom Resource Definitions (CRDs) are defined in api/${version}/*.go files. The CRDs are versioned as v1alpha1, v1alpha2, etc.

**For alpha versions**, the APIs will be marked as deprecated in the version N and will be removed in the version N+2.
Migration paths for alpha versions will be the best effort and will be documented in the release notes.

**For beta versions**, For beta versions, it is the same as the alpha versions, but we guarantee that we provide a migration path in the release notes.

**For stable versions**, we will never break the APIs unless there is a critical security issue.
We will provide a migration path in the release notes in case we need to break the APIs.

### Upgrading the Envoy AI Gateway controller

We guarantee that simply upgrading the controller will not break the existing configuration assuming there's no _un-migrated_ resources including breaking change left in the k8s API server. In other words, after the proper use of the API and migration path described above, the user should be able to upgrade the controller without any issue. However, this does mean that we do NOT guarantee that the existing configuration will work across more than two version of the controller. For example if you are using the version N of the controller, and you want to upgrade to the version N+2, you should first upgrade to the version N+1 while following the migration path if applicable, and then upgrade to the version N+2.

### Envoy Gateway vs Envoy AI Gateway compatibility

Since Envoy AI Gateway is built on top of Envoy Gateway, the compatibility between the two is important.

We use the latest released version of Envoy Gateway as the base of the Envoy AI Gateway when we release a new version.

Since Envoy Gateway is a stable project and supposed to work across versions, we do not expect any compatibility issue as long as the Envoy Gateway version is also up-to-date prior to the upgrade of the Envoy AI Gateway.

## Release Process

This section is for maintainers of the project. Let's say we are going to release the version v0.50.0.

### Release Candidate (RC) Phase

Each non-patch release should start with Release Candidate (RC) phase as follows:

1. First, notify the community that we are going to cut the release candidate and therefore the main branch is frozen.
  The main branch should only accept the bug fixes, the security fixes, and documentation changes.
  The release candidate should always be cut from the main branch.

2. Cut the request candidate tag from the main branch. The tag should be v0.50.0-rc1. Assuming the remote `origin` is the main envoyproxy/ai-gateway repository,
  the command to cut the tag is:
    ```
    git fetch origin # make sure you have the latest main branch locally.
    git tag v0.50.0-rc1 origin/main
    git push origin v0.50.0-rc1
    ```
   Pushing a tag will trigger the pipeline to build the release candidate image and the helm chart tagged with the release candidate tag.
   The release candidate image will be available in the Docker Hub.

3. The release candidate should be tested by the maintainers and the community. If there is any issue, the issue should be fixed in the main branch
  and the new rc tag should be created. For example, if there is an issue in the release candidate v0.50.0-rc1, replace `v0.50.0-rc1` with `v0.50.0-rc2`
  in the above command and repeat the process.

### Release Phase

1. Once the release candidate is stable, we will cut the release from the main branch, assuming that's exactly the same as the last release candidate.
  The command to cut the release is exactly the same as the release candidate:
    ```
    git fetch origin # make sure you have the latest main branch locally.
    git tag v0.50.0 origin/main
    git push origin v0.50.0
    ```
   Pushing a tag will trigger the pipeline to build the release image and the helm chart tagged with the release tag.
   The release image will be available in the Docker Hub.
2. The draft release note will be created in the GitHub repository after the pipeline is completed.
   Edit the release note nicely by hand to reflect the changes in the release.
3. Announce the release in the community.
4. Create `release/v0.50` branch from the tag for the future backports, bug fixes, etc.

### Backport Phase

1. If there is a bug fix or a security fix that needs to be backported to the previous release, maintainers should cherry-pick the commit and raise the PR to the `release/v0.50` branch.
   Which commit should be backported is up to the maintainers and on a case-by-case basis.
2. Once the PR is merged, the maintainers will decide when to cut the patch release. There's no need to wait for multiple backports to cut the patch release, etc.
   Do not cut the tag until all CI passes on the release/v0.50 branch.
3. The patch release should be cut from the `release/v0.50` branch. The command to cut the patch release is exactly the same as normal release:
    ```
    git fetch origin # make sure you have the latest release/v0.50 branch locally.
    git tag v0.50.1 origin/release/v0.50
    git push origin v0.50.1
    ```
   Pushing a tag will trigger the pipeline to build the patch release image and the helm chart tagged with the patch release tag.
   The patch release image will be available in the Docker Hub.
4. The draft release note will be created in the GitHub repository after the pipeline is completed.
   Edit the release note nicely by hand to reflect the changes in the release.
5. Update the documentation on the main branch to reflect the new version. This has the following items:
   * Change `v0.50.0` to `v0.50.1` in site/versioned_docs/version-0.50 directory.
   * Update the site/src/pages/release-notes.md to add the new release note.
