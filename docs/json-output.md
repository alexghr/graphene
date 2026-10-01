# JSON output

Use `graphene graph --json` to inspect the graph and pending work, or `graphene send --dry-run --json` / `graphene sendf --dry-run --json` to inspect the push plan. All support `--stack` with the same selection as their text output.

A successful command writes one JSON object to stdout. Warnings and Git diagnostics, including output from push hooks, go to stderr. Check the exit status before consuming the result. Push JSON requires `--dry-run`: it retains the normal push safeguards and Git dry-run check, which may contact the remote and run hooks, without updating refs or upstreams. A failed push check produces no JSON object.

Every object has `schema_version: 1`. Consumers should check this version and ignore additional fields. Breaking changes to documented fields require a new schema version. These objects are public inspection formats, independent of Graphene's stored state and recovery manifests.

## Graph

```json
{
  "schema_version": 1,
  "current_branch": "stack/one",
  "branches": [
    {"name": "main", "parent": "", "commit": "<full commit ID>", "tracked": false},
    {"name": "stack/one", "parent": "main", "commit": "<full commit ID>", "tracked": true}
  ],
  "pending": null
}
```

`branches` contains the visible graph nodes, including stack bases, in stack metadata order. Each branch has its name, direct tracked parent, full local commit ID, and whether it is a tracked stack branch. `parent` is empty for untracked bases; `commit` is empty if a branch is missing locally. `current_branch` is empty for a detached checkout. An empty graph has `branches: []`.

`pending` is `null` when no operation is recorded. Otherwise it contains `operation`, `branch`, `return_branch`, `phase`, and `rebases`. The first three identify the command, its subject branch, and its intended final branch. `phase` is the recorded recovery phase (`ready`, `applying`, `conflict`, `deleting`, or `aborting`), or an empty string for operations without recorded phases. An empty phase does not establish whether a Git rebase is active.

Each queued rebase has `branch`, `onto`, and `upstream`: the branch to replay, its destination revision, and its source boundary revision. Rebases appear in execution order, including an active step. A pending operation can have `rebases: []`, for example while waiting for split commits or finalizing sync. `--stack` filters graph nodes but retains the repository's pending operation.

## Push plan

```json
{
  "schema_version": 1,
  "current_branch": "stack/one",
  "remote": "origin",
  "scope": "current branch and tracked ancestors",
  "atomic": true,
  "force_with_lease": true,
  "dry_run": true,
  "branches": ["stack/one"]
}
```

`branches` is the exact ordered branch selection passed to Git. `remote` is the selected remote, including inferred defaults. `atomic`, `force_with_lease`, and `dry_run` describe the push flags. `scope` is explanatory text; use `branches` to determine the selection rather than parsing that text. `current_branch` identifies the checkout used to select the plan.
