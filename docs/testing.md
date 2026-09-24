# Testing

Run the full suite with `./shell p-test`, or lint, test, and build with
`./shell p-ci`. CI and the production Nix build run all three test layers.

| Layer | Devenv command | Plain Go command |
| --- | --- | --- |
| Unit | `./shell p-test-unit` | `go test -parallel 8 -run '^TestUnit' ./internal/...` |
| Integration | `./shell p-test-integration` | `go test -parallel 8 -skip '^TestUnit' ./internal/...` |
| E2E | `./shell p-test-e2e` | `go test -parallel 8 ./tests/e2e` |

`go test ./...` also runs every layer. Use `-count=1` when measuring uncached test
execution. The layered commands run sequentially so each timing is useful and the
Git-heavy packages do not compete for two separate sets of eight test slots.

## Where a test belongs

Unit tests use the `TestUnit` prefix. They test parsing, graph/state transformations,
selection policies, rendering, or a small filesystem invariant directly. They do
not start Git or replace it with a mock. Keep edge cases in these cheap tests when
Git semantics are irrelevant.

Integration tests under `internal` cover contracts that require real Git or OS
behavior: index fidelity, atomic refs, fetch destinations, persistence, locking,
and recovery boundaries. Use small independent repositories. A real failing Git
hook is useful fault injection; do not simulate successful Git behavior.

E2E tests under `tests/e2e` build the actual CLI once and invoke it in separate
processes against local repositories and bare remotes. They own complete user flows
and assert content, history, push scope, and preservation of unrelated work. Build
stacks through the CLI rather than manufacturing Graphene state. Success scenarios
must succeed; expected failures must check the specific reason and preserved state.

## Avoiding overlap

Choose the smallest layer that proves an invariant. Unit tables own branch-selection
permutations; an E2E owns the proof that selected branches actually reach the remote.
Keep an integration test between them only when it tests a distinct Git contract.

The priority workflows are new/send/amend/sendf/cleanup, sync from a stack tip,
sync-all from a base with multiple stacks, and restack from the current branch
upwards. Some E2Es use eight-commit stacks and forks; component tests should use the
minimum history that distinguishes their failure case.

Recovery assertions must match the command's contract. Sync/restack abort restore
the operation's snapshot. Amend commits the edit before rebasing descendants, so
its abort retains that amended commit and cancels the descendant rebase; it does
not restore the pre-amend staged edit.

When replacing a test, transfer its important assertions before deleting it. Count
executed scenarios, not just functions: combining many cases into one table does
not reduce runtime or overlap. Parallelize independent parent groups as well as
their subtests. Do not share mutable repositories or parallelize process-global
environment changes.

The original classification and workflow specifications are in
[the cleanup plan](test-suite-plan.md).
