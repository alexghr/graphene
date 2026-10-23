# JSON and rewrite inspection

For machine-readable inspection, prefer `graphene graph --json` (optionally `--stack`) for branches and pending work. When a checked push plan is needed, use `graphene sendf --stack --dry-run --json`. `send` also supports `--dry-run --json`. Read the JSON object from stdout and diagnostics from stderr; check the exit status and `schema_version`. Push JSON requires `--dry-run` and does not update refs or upstreams.

After a rewrite finishes, inspect the summary of old and new branch tips. `patch unchanged` compares the branch's own net textual patch, preserving whitespace and ignoring shifted line positions, inherited blob hashes and commit messages; it does not mean the branch's whole tree is unchanged. Binary patches and unavailable historical boundaries report `patch comparison unavailable`. A successful `graphene continue` reports against the original tips.

