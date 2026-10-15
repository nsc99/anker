# Repository Guidelines

## Project Structure & Module Organization

- `cmd/anker/main.go` starts the HTTP service and handles graceful shutdown.
- `internal/exporter/` contains environment configuration, Anki protocol requests, snapshot management, statistics, and HTTP handlers.
- `internal/exporter/exporter_test.go` tests statistics and synchronization with temporary SQLite collections and a local HTTP server.
- `flake.nix` and `flake.lock` define development tooling and the reproducible package; `go.mod` and `go.sum` pin Go dependencies.
- `README.md` documents configuration, API behavior, and Homepage integration. `.env.example` provides configuration placeholders.

## Build, Test, and Development Commands

Use Nix flakes for tooling; avoid Homebrew or global Go installations.

- `nix develop`: enter the Go and gopls environment with CGO support.
- `go run ./cmd/anker`: run locally inside that shell after exporting configuration.
- `gofmt -w cmd internal`: format Go source inside the shell.
- `nix develop --command go test -race ./...`: run all tests with race detection.
- `nix develop --command go vet ./...`: run static checks.
- `nix build`: build `result/bin/anker`, running package tests and vet checks.
- `nix flake check --no-build`: validate flake outputs without building.

New source and flake files must be visible to Git for Nix's Git source filter.

## Feature Worktrees

Always create a dedicated Git worktree and feature branch before implementing a new feature. Make all feature changes and run validation in that worktree. For example: `git worktree add -b feature/stats-filter ../anker-stats-filter HEAD`. Preserve existing uncommitted changes in the original checkout. Keep the worktree until the feature is integrated or explicitly abandoned; remove it only after confirming its changes are preserved.

## Coding Style & Naming Conventions

Follow idiomatic Go and `gofmt` indentation. Use lowercase package names, descriptive identifiers, and `TestBehavior` names in `*_test.go`. Keep implementation within `internal/exporter` and startup wiring in `cmd/anker`. Preserve snake_case JSON fields and the `ANKER_` environment-variable prefix.

## Testing Guidelines

Use Go's standard testing package and `httptest`. Add behavior tests for protocol changes, failed-refresh recovery, retention calculations, rollover, and daylight saving boundaries. Use temporary files and local servers; avoid live credentials. No coverage percentage is currently enforced.

## Commit & Pull Request Guidelines

Always use Conventional Commits: `type(scope): description`, with an optional scope and a concise imperative description. Examples: `feat(stats): add retention calculation`, `fix(sync): preserve the last valid snapshot`, and `docs: clarify configuration`.

Commit changes in small, incremental functional steps. Each commit must represent one coherent, complete change and leave the repository buildable and its relevant checks passing. Include the tests and documentation needed for that step in the same commit. Avoid commits containing unfinished wiring, known failures, or unrelated changes. Validate each step before committing so it can be reviewed or reverted independently.

PRs should explain the problem, resulting behavior, relevant issues, and validation commands. Update documentation when configuration or API behavior changes.

## Configuration, Safety, and Versions

Keep credentials, collections, and caches out of Git. The service does not automatically load `.env`. Preserve private snapshots and last-good data on refresh failures; never upload changes or open the sync server's live database.

Never write versions from memory. Prefer existing pins; verify changes against the pinned nixpkgs, module registry, or upstream releases, and report the version and source. Update dependency lock files and the Nix vendor hash together when needed.
