package graphenestackedprs

import "embed"

// Content is the bundled Graphene stacked PRs skill.
//
//go:embed SKILL.md
var Content string

// References contains the skill's supporting Markdown files.
//
//go:embed references/*.md
var References embed.FS
