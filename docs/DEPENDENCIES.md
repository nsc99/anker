# Dependency maintenance

Renovate manages Go modules, GitHub Actions, and the nixpkgs flake input.
Routine updates are scheduled for Mondays between midnight and 07:00 in
Europe/Berlin, with at most three regular pull requests open at once. Minor
and patch module updates are grouped together; routine Action updates form
a separate group. Major upgrades are proposed individually. Updates require
manual merging after review and successful CI. The Dependency Dashboard
provided by `config:recommended` lists outstanding updates.

GitHub vulnerability-alert fixes bypass the regular schedule and PR limits
when the repository's alerts and Renovate integration support them. Review
these promptly using the same build checks.

## Go modules and the Nix vendor hash

Renovate runs `go mod tidy` to maintain `go.mod` and `go.sum` and filters module
releases against the Go requirement declared in `go.mod`. Go version directives
are not updated independently: the actual toolchain comes from pinned nixpkgs.
When updating nixpkgs, check whether `go.mod` needs a coordinated version change.

Go dependency changes can also change Nix's vendored dependency hash. Hosted
Renovate does not automatically recalculate this repository's `vendorHash`, so
module PRs include a reminder to update it in the same PR:

1. Check out the dependency PR and review the proposed module versions.
2. Temporarily set `vendorHash = pkgs.lib.fakeHash;` in `flake.nix`.
3. Run `nix build .#default.goModules --no-link`. This intentionally fails with
   a hash mismatch that reports the actual dependency hash.
4. Replace the placeholder with the reported hash, then run `nix build`.
5. Commit `flake.nix` together with the updated `go.mod` and `go.sum`. Both native
   Linux image builds and container smoke tests must pass before merging.

Use the development shell for any manual Go commands. Do not merge a module PR
while its Nix vendor hash is out of date.

## Nix and GitHub Actions

The nixpkgs input explicitly tracks `nixpkgs-unstable`; `flake.lock` pins its exact
revision. Weekly Nix lock-file maintenance updates that locked revision while
retaining the channel. Adding the channel metadata did not change the existing
locked revision or package versions.

Action references stay pinned to full commit SHAs, with version comments for
readability. Renovate manages both the version and digest updates.

References:

- [Renovate Nix support](https://docs.renovatebot.com/modules/manager/nix/)
- [Renovate Go module support](https://docs.renovatebot.com/modules/manager/gomod/)
- [Renovate Action SHA pinning](https://docs.renovatebot.com/presets-helpers/#helperspingithubactiondigests)
