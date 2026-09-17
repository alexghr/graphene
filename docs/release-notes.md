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
