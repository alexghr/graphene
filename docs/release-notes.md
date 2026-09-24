# Release notes

Use this format for GitHub release bodies, with headings in this order:

```markdown
## Changes

- Describe new features, improvements, and fixes, including relevant commands or flags.

### Breaking changes

- Explain what changed and how users should update their commands or scripts.

## Misc

- Describe relevant documentation or maintenance changes.

[Full changelog](https://github.com/alexghr/graphene/compare/vPREVIOUS...vCURRENT)
```

- Use `Changes` for new features, improvements, and fixes. Do not introduce
  separate `Added` or `Fixed` headings.
- Nest `Breaking changes` under `Changes` as a level-three heading. Omit it
  when there are no breaking changes; do not add a `None.` placeholder.
- Use the optional level-two `Misc` heading for relevant documentation or
  maintenance notes. Omit it when there is nothing to add.
- Use concise bullets describing observable behavior. Include migration guidance
  for removed options, changed defaults, and other compatibility changes.
- Write for people using Graphene. Lead with what gets easier, what now works,
  or what they need to do differently. Prefer familiar words over implementation
  terms: "Recovery checks are faster in large repositories" rather than
  "reducing rollback preflight work."
- Keep each bullet focused on one user-relevant change or a closely related group.
  Usually one sentence is enough; add another when users need an action or caveat.
  Keep distinct breaking changes separate so migration steps are easy to scan.
- Explain internal changes through their practical benefit. Include storage paths,
  algorithms, and recovery machinery only when users need them to act. Put supporting
  code references and technical investigation in the separate reviewer section.
- Describe each change once, in its most relevant section. Keep useful command
  examples and links. Omit routine refactors, tests, dependency updates, and version
  bumps unless they have a user-facing consequence.
- End with one `Full changelog` link in the form above. For the first release,
  link to the tag's commit history instead of a comparison.
- Verify notes against the exact release tags and relevant code or documentation.
  Historical notes must describe behavior at that release, not current behavior.
- When reformatting existing releases, preserve their meaning and comparison
  ranges. Do not backfill releases containing only a changelog link unless asked.

The release workflow generates GitHub notes automatically; those are a starting
point, not the final format. Review and format them using this guide when preparing
or updating a release. Use `gh release edit --notes-file <path>` to upload the
reviewed body, then read it back to verify it. A notes-only update should preserve
the release title, tag, assets, and publication status.

## Drafting with Codex

The project agent [`release_notes`](../.codex/agents/release_notes.toml) uses Luna
to draft or reformat notes. This guide remains the source of editorial rules.
Give the agent the exact previous and target refs, or identify the target as the
first release. For reformatting, also provide the existing release body.

For example: "Use the release_notes agent to draft notes for v0.5.0 through
v0.6.0, checking the relevant diffs against docs/release-notes.md."

The agent returns a Markdown release body plus separate evidence and unresolved
questions for review. It reads the repository without modifying files or releases.
Before returning a draft, account for each non-version commit as included, grouped
with another change, or omitted with a reason. Keep this coverage check in the
reviewer section, and check that compatibility changes and recovery instructions
have not disappeared while shortening the release body.
The parent agent reviews the draft, uses Sol for deeper investigation if needed,
and saves or publishes the notes within the user's authorized scope. Publishing
and read-back verification follow the process above.
