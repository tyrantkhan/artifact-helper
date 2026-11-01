// Package skills embeds the agent skill, so the install command can write it where each host reads skills.
package skills

import _ "embed"

// Name is the skill's directory name under a host's skills directory.
const Name = "claude-artifact-helper"

// ClaudeArtifactHelper is skills/claude-artifact-helper/SKILL.md.
//
//go:embed claude-artifact-helper/SKILL.md
var ClaudeArtifactHelper []byte
