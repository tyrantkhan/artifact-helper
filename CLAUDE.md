# artifact-helper

A Go CLI that Claude Code runs from its shell to publish, read, edit, share and answer comments on claude.ai
artifacts. Each command runs Claude Code headlessly (`claude -p`) with only its Artifact tool (plus Read, to
publish a file) and a prompt that spells out the one call to make; the run answers through `--json-schema`.
The module has no dependencies. Used from Claude Code only: there is no MCP server, by decision.

## Build, test, lint

- `mise run build` → `bin/artifact-helper` with the git version baked in; `mise run test`; `mise run lint`.
  Plain `go build ./cmd/artifact-helper`, `go test ./...` and `golangci-lint run` do the same.
- `mise run dev -- <args>` builds this checkout and runs it. Never `go install` a dev build: mise puts Go's bin
  first on PATH, where it shadows the released binary that `install.sh` puts in `~/.local/bin`.
- Tests never call the real `claude`: `internal/fakeclaude` copies the test binary to a temporary `claude`
  that answers by the `Task: <op>.` marker that starts every prompt, on every system (`fakeClaude` in
  `internal/headless/headless_test.go`). Keep that marker first in any new prompt.
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
- `claude -p` leaves the Artifact tool off unless `CLAUDE_CODE_ARTIFACT=1` (Claude Code's `sdk_default_off`);
  the run then answers that the tool is not available. `run` sets it unless the environment already does, and
  the fake claude fails without it, so every test checks it.
- Every run passes `--permission-prompts none` so anything that would prompt is denied. The Artifact tool's
  `edits` argument needs a file-edit permission that this denies, so `edit` applies edits in Go and republishes.
- A share is confirmed by the person on a card; the share run alone uses `bypassPermissions` and is gated
  behind `ARTIFACT_HELPER_ALLOW_SHARE`. A test asserts publishing never bypasses.
- Comments live in the Artifact tool as actions `comments`, `reply` and `resolve`; the separate
  `ArtifactComments` tool does not exist in the local CLI. Reply and resolve work only on a thread a person
  sent to Claude; the `not-activated` detection is a regex that has not yet seen the real wording.
- `claude auth status` carries `email` and `orgName` only for a claude.ai login; a bare token has neither,
  which is why `ARTIFACT_HELPER_ORGANIZATION` refuses with `organization-unverifiable` rather than guessing.
- Runs stream (`--output-format stream-json --verbose`). The last line is the result, with `structured_output`
  when `--json-schema` is passed; `decode` reads it. Each tool result event before it carries the tool's own
  data in `tool_use_result`, which `list` and `files` read instead of having the model retype the listing:
  the retyping took 28 of 32 seconds. Tests build such a stream with `fakeclaude.ToolRun`.

## Conventions

- Conventional Commits; branches `<type>/<issue>-<description>` (lefthook checks both). release-please cuts
  releases from `main` and bumps `internal/cli/cli.go` and `.claude-plugin/plugin.json`.
- `main` takes squash merges only, behind a ruleset like sift's. The PR title becomes the commit
  release-please reads, so `pr-title.yml` checks it and requires the highest-priority type of its commits.
- Commit messages, PR bodies and code carry no model identifiers.
- Prose in help text, README and the guide: short sentences, no em-dashes, one idea each, as the rest reads.
