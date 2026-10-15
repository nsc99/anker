# Releasing Anker

This repository publishes containers through `.github/workflows/publish-images.yml`.
Pull requests build and smoke-test both Linux architectures without publishing.
Default-branch pushes publish rolling `latest` and commit tags; GitHub release
publication builds the release's tagged revision and publishes its exact version.
Manual workflow runs publish commit tags only. Version releases never move
`latest`, which always represents the default branch.

Recommended release procedure:

1. Merge the intended changes and wait for both platform builds and container
   smoke tests to succeed. Review configuration changes and migration needs.
2. Choose a new semantic version: major for incompatible changes, minor for
   compatible features, patch for compatible fixes. The workflow accepts
   `vMAJOR.MINOR.PATCH` and prerelease suffixes such as `-rc.1`. Verify existing
   tags before selecting the next version; do not reuse a published tag.
3. Draft a GitHub release targeting the tested commit. Generate notes as a
   starting point, then explain the resulting behavior, breaking changes,
   configuration or deployment steps, and known limitations. Mark release
   candidates as prereleases. Attach any GitHub release assets before publishing.
4. Publish the release and watch the image workflow. Both architectures must
   pass before the publishing job can run. Verify the version image is available
   from GHCR before announcing or deploying it.
5. Record and deploy the multi-platform image digest. Keep the previous working
   digest and database backup available for rollback. If a release needs a fix,
   publish a new version rather than changing the old tag.

Consider enabling immutable releases in GitHub repository settings. GitHub
releases are based on tags, so protecting the released tag preserves its source
identity. Drafting first allows all assets to be attached before immutability
applies. This workflow publishes external GHCR images after release publication
and does not append GitHub release assets.

The existing workflow limits permissions, pins Actions to commit SHAs, uses the
short-lived `GITHUB_TOKEN`, and builds with locked Nix and Go dependencies. Keep
these properties when updating it. For stronger supply-chain assurance, consider
adding signed provenance and an SBOM in a future change. GHCR package visibility
and repository release settings are managed separately from the workflow.

References:

- [GitHub releases and tags](https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases)
- [Generated release notes and drafting immutable releases](https://docs.github.com/en/repositories/releasing-projects-on-github/automatically-generated-release-notes)
