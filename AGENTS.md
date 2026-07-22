# AGENT.md

## Project

Graphene is a small Go CLI for managing stacked PR branches.

- Entry point: `cmd/graphene/main.go`.
- Core code: `internal/graphene`.
- State is stored in `<git-common-dir>/graphene/state.json`; legacy local Git config under `graphene.state` is migrated automatically.
- Prefer the Go standard library. Ask before adding dependencies.
- Production binaries are built with Nix through `devenv.nix`.

## Commands

- Test: `go test ./...`
- Devenv test script: `./devenv shell graphene-test`
- Local binary script: `./devenv shell graphene-build`
- Production build: `./devenv build`

## Code Style

- Keep code simple, deliberate, and easy to read.
- Prefer clear names and small functions over comments.
- Add comments only for surprising Git behavior or non-obvious state transitions.
- Do not over-validate. Validate where bad input could corrupt state, create invalid refs, or make Git fail unclearly.
- Keep CLI behavior explicit and boring. Preserve Git's own output when commands stream to the terminal.
- Avoid clever abstractions unless they remove real repetition or clarify the stack model.

## Tests

- Keep tests brief and targeted.
- Do not chase code coverage.
- Avoid mocks. Prefer real Git repos, temp dirs, and small pure helper tests.
- Test important branch, state, rebase, push, and URL behavior.
- Use Git-backed integration tests when behavior depends on real Git semantics.
- Use small unit tests for pure helpers such as parsing, state transforms, slugs, graph rendering, and PR URLs.
