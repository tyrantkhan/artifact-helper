// Package skillguides embeds the version-matched guides that `artifact-helper get-skill` prints. The stub a host installs
// (skills/<name>/SKILL.md) tells the agent to load its guide from the binary, so the details here never
// drift from the version that runs.
package skillguides

import _ "embed"

// ArtifactHelper is skill-guides/artifact-helper.md.
//
//go:embed artifact-helper.md
var ArtifactHelper []byte
