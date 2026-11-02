# artifact-helper

A Go CLI that Claude Code runs from its shell to publish, read, edit, share and answer comments on claude.ai
artifacts. Each command runs Claude Code headlessly (`claude -p`) with only its Artifact tool (plus Read, to
publish a file) and a prompt that spells out the one call to make; the run answers through `--json-schema`.
The module has no dependencies. Used from Claude Code only: there is no MCP server, by decision.

## Build, test, lint

- `mise run build` → `bin/artifact-helper` with the git version baked in; `mise run test`; `mise run lint`.
  Plain `go build ./cmd/artifact-helper`, `go test ./...` and `golangci-lint run` do the same.
- Tests never call the real `claude`: a fake shell script answers by the `Task: <op>.` marker that starts
  every prompt (`fakeClaude` in `internal/headless/headless_test.go`). Keep that marker first in any new prompt.
- golangci-lint must be built with a Go at least as new as `go.mod`'s, or it refuses to load the config.
- CI: `.github/workflows/ci.yml` (tests on Linux, macOS, Windows, with coverage and results sent to Codecov
  and gated by `codecov.yml`; lint; govulncheck; shellcheck on `install.sh`, `hooks/session-start` and
  `.github/scripts/plugin-files.sh`).

## Layout

- `cmd/artifact-helper` is `main`; `internal/cli` is the command table (an `Env` with injected stdio, every
  command prints one JSON object, a refusal is `{"ok": false, "error": {code, message, fix}}`).
- `internal/headless` drives `claude -p`: `run` (flags, timeout, parsing), one function per operation, and
  the `Refusal` type with its stable codes. Add a new operation there, then a command in `internal/cli`.
- `internal/skillinstall` installs the skill into Claude Code's skills folder, Orca-style: only an untouched
  copy of ours is rewritten (recognised by `skills/released-digests.txt`); edited or foreign copies are left
  alone unless `--force`, which backs them up.
- `skills/artifact-helper/SKILL.md` is the thin stub a host installs; `skill-guides/artifact-helper.md` is
  the guide `get-skill` prints, embedded. **When the stub changes, append its new sha256 (LF line endings) to
  `skills/released-digests.txt`**; a test fails until you do. The guide can change freely.
- `.claude-plugin/` is the Claude Code plugin (skill + `hooks/session-start`); `claude plugin validate .`
  must pass. Plugin names may not start with `claude-`, which is why everything is `artifact-helper`.

## Facts learned the hard way

- `claude --bare` disables OAuth, so a subscription login stops working; never pass it.
- Every run passes `--permission-prompts none` so anything that would prompt is denied. The Artifact tool's
  `edits` argument needs a file-edit permission that this denies, so `edit` applies edits in Go and republishes.
- A share is confirmed by the person on a card; the share run alone uses `bypassPermissions` and is gated
  behind `ARTIFACT_HELPER_ALLOW_SHARE`. A test asserts publishing never bypasses.
- Comments live in the Artifact tool as actions `comments`, `reply` and `resolve`; the separate
  `ArtifactComments` tool does not exist in the local CLI. Reply and resolve work only on a thread a person
  sent to Claude; the `not-activated` detection is a regex that has not yet seen the real wording.
- `claude auth status` carries `email` and `orgName` only for a claude.ai login; a bare token has neither,
  which is why `ARTIFACT_HELPER_ORGANIZATION` refuses with `organization-unverifiable` rather than guessing.
- Headless results come back with `structured_output` when `--json-schema` is passed; `decode` reads it.

## Conventions

- Conventional Commits; branches `<type>/<issue>-<description>` (lefthook checks both). release-please cuts
  releases from `main` and bumps `internal/cli/cli.go` and `.claude-plugin/plugin.json`.
- `main` takes squash merges only, behind a ruleset like sift's. The PR title becomes the commit
  release-please reads, so `pr-title.yml` checks it and requires the highest-priority type of its commits.
- Commit messages, PR bodies and code carry no model identifiers.
- Prose in help text, README and the guide: short sentences, no em-dashes, one idea each, as the rest reads.
