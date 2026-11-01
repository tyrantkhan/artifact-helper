// Package skillguides embeds the version-matched guides that `skills get` prints. The stub a host installs
// (skills/<name>/SKILL.md) tells the agent to load its guide from the binary, so the details here never
// drift from the version that runs.
package skillguides

import _ "embed"

// ClaudeArtifactHelper is skill-guides/claude-artifact-helper.md.
//
//go:embed claude-artifact-helper.md
var ClaudeArtifactHelper []byte
