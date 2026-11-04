# artifact-helper

A Go CLI that Claude Code runs from its shell to publish, read, edit, share and answer comments on claude.ai
artifacts. Each command runs Claude Code headlessly (`claude -p`) with only its Artifact tool (plus Read, to
publish a file) and a prompt that spells out the one call to make; the run answers through `--json-schema`.
The module has no dependencies. Used from Claude Code only: there is no MCP server, by decision.

## Build, test, lint

- `mise run build` → `bin/artifact-helper` with the git version baked in; `mise run test`; `mise run lint`.
  Plain `go build -o bin/artifact-helper ./cmd/artifact-helper`, `go test ./...` and `golangci-lint run` do
  the same. A bare `go build ./cmd/artifact-helper` leaves the binary at the repo root.
- `mise run dev -- <args>` builds this checkout and runs it. Never `go install` a dev build: mise puts Go's bin
  first on PATH, where it shadows the released binary that `install.sh` puts in `~/.local/bin`.
- Tests never call the real `claude`: `internal/fakeclaude` copies the test binary to a temporary `claude`
  that answers by the `Task: <op>.` marker that starts every prompt, on every system (`fakeClaude` in
  `internal/headless/headless_test.go`). Keep that marker first in any new prompt.
- golangci-lint must be built with a Go at least as new as `go.mod`'s, or it refuses to load the config. When the
  installed one is older, `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run` builds CI's
  version with this Go without installing anything.
- CI: `.github/workflows/ci.yml` (tests on Linux, macOS, Windows, with coverage and results from each sent to
  Codecov and gated by `codecov.yml`; lint; govulncheck; shellcheck on `install.sh`, `hooks/session-start` and
  `.github/scripts/*.sh`; `check-commit-types.sh`, which fails when the places listing commit types disagree).
  Every action is pinned to a commit SHA with its version beside it; Dependabot moves the pins.

## Layout

- `cmd/artifact-helper` is `main`; `internal/cli` is the command table (an `Env` with injected stdio, every
  command prints one JSON object, a refusal is `{"ok": false, "error": {code, message, fix}}`).
- `internal/cli/record.go` keeps `published.json` beside the config: each artifact this machine published or read,
  keyed by link, with the local files holding its page (by real path) and the version each holds, the sessions that
  published it (`ARTIFACT_HELPER_SESSION`, else `CLAUDE_CODE_SESSION_ID`, which Claude Code sets for every command
  it runs), and `movedTo` after `--new`. publish and edit resolve the link from it, so callers never manage links;
  `updates` reports a session's artifacts from it. A publish passes the file's own version as `ExpectVersion`, so a
  read of another copy cannot let a stale file through. A file with no version of its own, one the record lacks or
  one published when the tool reported none, goes over the artifact's newest.
- `init` saves a config (`internal/cli/config.go`) that every command but `get-skill` and `init` requires
  (`not-initialized`); environment variables and flags override it. Tests MUST set `Env.Config` to a
  temporary path (`savedConfig`), so no test reads or writes the real one. `init` and `doctor` print a
  readable view only when `Env.Terminal` says so (`internal/cli/pretty.go`), standard library only.
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
- Every run but share passes `--permission-prompts none`, so anything that would prompt is denied. Runs that
  only answer also use `--permission-mode default`, which denies even a file write, so text written by other
  people cannot get a file saved; only read uses `acceptEdits`, in an empty folder of its own. Publish and list
  work without `acceptEdits` (checked against the real CLI). The Artifact tool's `edits` argument needs a
  file-edit permission, so `edit` applies edits in Go and republishes.
- A run with no folder of its own starts in an empty temporary folder, never the caller's project. `checkRuns`
  in the headless tests holds every run to its permission level and folder.
- A share is confirmed by the person on a card; the share run alone uses `bypassPermissions` and is gated
  behind `ARTIFACT_HELPER_ALLOW_SHARE`.
- Comments live in the Artifact tool as actions `comments`, `reply` and `resolve`. Claude Code 2.1.316 lists
  them under its `ArtifactComments` tool but still accepts them on `Artifact` (checked). Reply and resolve work
  only on a thread a person sent to Claude; the `not-activated` detection matches the tool's own text, whose
  wording in 2.1.316 is "not activated on this thread".
- Results come from the Artifact tool's own output in the stream wherever it has one: publish's link and
  `updated`, comments' `raw`. The model's prose can name a link while it explains a failure, so it is never
  scraped for one.
- `claude auth status` reports `orgId` beside `orgName`; init saves both, and the check compares the ID.
- A `-p` run with `--no-session-persistence` adds no project entry to `~/.claude.json` (checked), so starting
  each run in a new temporary folder leaves nothing behind.
- Publish reads its result from the Artifact call whose action is `publish`: an update calls `read` first, and
  `streamTools` records each call's action so the read is never taken for the publish.
- The fake claude refuses to write its `Files` outside the temporary folder, since a test once leaked
  `internal/cli/index.html` into the tree.
- `claude auth status` carries `email` and `orgName` only for a claude.ai login; a bare token has neither,
  which is why `ARTIFACT_HELPER_ORGANIZATION` refuses with `organization-unverifiable` rather than guessing.
- Every `claude` the tool starts inherits `CLAUDE_CONFIG_DIR`, so a calling session that sets it publishes as
  that folder's account. `ARTIFACT_HELPER_CLAUDE_CONFIG_DIR=default` drops it. Dropping is the only way back to
  the default login: Claude Code picks the keychain entry by whether the variable is set, not by its value.
- Claude Code's Bash tool gives commands no terminal on stdin or stdout, so terminal detection is what keeps
  agents on JSON while a person at a terminal gets the readable view.
- Runs stream (`--output-format stream-json --verbose`). The last line is the result, with `structured_output`
  when `--json-schema` is passed; `decode` reads it. Each tool result event before it carries the tool's own
  data in `tool_use_result`, which `list` and `files` read instead of having the model retype the listing:
  the retyping took 28 of 32 seconds. Tests build such a stream with `fakeclaude.ToolRun`.

## Conventions

- Conventional Commits; branches `<type>/<issue>-<description>` (lefthook checks both). release-please cuts
  releases from `main` and bumps `internal/cli/cli.go` and `.claude-plugin/plugin.json`.
- Every merge to `main` cuts a release. release-please skips a release whose changelog would be empty, so every
  commit type lefthook allows has a visible section in `release-please-config.json`; keep it that way. The
  guide ships inside the binary, which is why a `docs:` change needs a release too.
- `main` takes squash merges only, behind a ruleset like sift's. The PR title becomes the commit
  release-please reads, so `pr-title.yml` checks it and requires the highest-priority type of its commits.
- Commit messages, PR bodies and code carry no model identifiers.
- Prose in help text, README and the guide: short sentences, no em-dashes, one idea each, as the rest reads.
