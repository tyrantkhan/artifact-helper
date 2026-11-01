# claude-artifact-helper

[![CI](https://github.com/tyrantkhan/claude-artifact-helper/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/tyrantkhan/claude-artifact-helper/actions/workflows/ci.yml)
[![Release](https://github.com/tyrantkhan/claude-artifact-helper/actions/workflows/release.yml/badge.svg?branch=main)](https://github.com/tyrantkhan/claude-artifact-helper/actions/workflows/release.yml)
[![Latest release](https://img.shields.io/github/v/release/tyrantkhan/claude-artifact-helper)](https://github.com/tyrantkhan/claude-artifact-helper/releases/latest)

Publish a file as a [claude.ai artifact](https://claude.ai/code/artifacts) from any agent, through the Claude subscription you already have.

Claude Code can publish an HTML page as an artifact: a private web page on claude.ai, shareable by its link. Other agents cannot. claude-artifact-helper is a stdio MCP server, a single Go binary, that gives Codex, Cursor, Gemini CLI, Claude Desktop or any other MCP client one tool, `publish_artifact`. Under the cover it runs Claude Code headlessly (`claude -p`) with only its Artifact tool enabled, lets that run do the upload, and hands back the link. No API key, no token of its own: the publish rides on the `claude` login on your machine.

## How it works

1. Your agent writes the page to a file and calls `publish_artifact` with its path.
2. The server starts `claude -p` in the file's directory, with `Read` and `Artifact` as its only tools, no permission prompts, no skills, no MCP servers, no saved transcript, and a prompt that spells out the exact Artifact call to make.
3. That run reads the file, publishes it, and answers with the link in a JSON schema the server asked for. The server checks the link and returns it.

A publish takes under a minute on a small page and uses a few model turns on `sonnet` by default. On a Claude subscription the cost Claude Code reports is notional. A larger page costs more, because the headless run reads the whole file before publishing it.

## Install

```sh
# Script: installs the latest release to ~/.local/bin after checking its checksum
curl -sSL https://raw.githubusercontent.com/tyrantkhan/claude-artifact-helper/main/install.sh | sh

# Homebrew (macOS and Linux)
brew install --cask tyrantkhan/tap/claude-artifact-helper

# mise
mise use -g github:tyrantkhan/claude-artifact-helper

# Go
go install github.com/tyrantkhan/claude-artifact-helper/cmd/claude-artifact-helper@latest
```

Each [release](https://github.com/tyrantkhan/claude-artifact-helper/releases) also has archives for macOS, Linux and Windows on amd64 and arm64, with a `checksums.txt`.

You also need [Claude Code](https://claude.com/claude-code) signed in to a Claude subscription:

```sh
claude auth login          # once, on this machine
claude-artifact-helper doctor
```

On a machine without a browser, run `claude setup-token` on a machine that has one and set the token it prints as `CLAUDE_CODE_OAUTH_TOKEN` in the MCP server's environment.

## Add the server to your agent

The server is launched as `claude-artifact-helper serve` and speaks MCP over stdin and stdout.

<details>
<summary>Codex</summary>

```sh
codex mcp add claude-artifact-helper -- claude-artifact-helper serve
```

</details>

<details>
<summary>Cursor</summary>

In `~/.cursor/mcp.json` (or the project's `.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "claude-artifact-helper": {
      "command": "claude-artifact-helper",
      "args": ["serve"]
    }
  }
}
```

</details>

<details>
<summary>Gemini CLI</summary>

```sh
gemini mcp add claude-artifact-helper claude-artifact-helper serve
```

</details>

<details>
<summary>Claude Desktop</summary>

In `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "claude-artifact-helper": {
      "command": "claude-artifact-helper",
      "args": ["serve"]
    }
  }
}
```

</details>

<details>
<summary>Claude Code</summary>

Claude Code publishes artifacts with its own Artifact tool when the account can; the server is for sessions where that tool is missing.

```sh
claude mcp add claude-artifact-helper -- claude-artifact-helper serve
```

</details>

<details>
<summary>Any other MCP client</summary>

Command `claude-artifact-helper`, arguments `["serve"]`, transport stdio. Put configuration in the server's environment (below).

</details>

If the binary is not on the PATH the client starts servers with, give its full path as the command.

## Tools

| Tool | Input | Result |
| :-- | :-- | :-- |
| `publish_artifact` | `file_path` (absolute path of a `.html` or `.md` file), optional `title`, `description`, `icon`, and `url` to republish an existing artifact in place | `url`, `updated`, `turns`, `cost_usd` |
| `check_setup` | none | the `claude` binary, its version, whether it is signed in, and the model and timeout a publish would use |

`title` is used when the page has no `<title>`; `description` is one sentence for the gallery card; `icon` is one generic word for the browser tab, such as `chart` or `map` (default `page`). Pass the `url` a publish returned to update that artifact instead of creating another.

A failed call returns `isError` with a JSON object as its text: `code`, `message` and `fix`. The agent should follow `fix`. Codes: `invalid-file`, `invalid-url`, `no-claude`, `not-logged-in`, `publish-refused` (the Artifact tool said no, with its reason), `publish-failed`, `timeout`, `claude-failed`, `bad-output`.

## Command line

The same publish is available from a shell, for agents that run commands instead of MCP tools. Every command prints one JSON object.

```sh
claude-artifact-helper publish report.html --title "Q3 report" --icon chart
claude-artifact-helper publish report.html --url https://claude.ai/artifact/<id>   # republish in place
claude-artifact-helper doctor
claude-artifact-helper help <command>
```

## Configuration

Set these in the environment the MCP client starts the server with, or pass the matching flag (`--claude`, `--model`, `--timeout`, `--max-budget-usd`, `--verbose`) to `serve` or `publish`.

| Variable | Default | Meaning |
| :-- | :-- | :-- |
| `CLAUDE_ARTIFACT_HELPER_CLAUDE` | `claude` on PATH | Path of the Claude Code binary |
| `CLAUDE_ARTIFACT_HELPER_MODEL` | `sonnet` | Model for the headless run; `haiku` is cheaper, `opus` rarely needed |
| `CLAUDE_ARTIFACT_HELPER_TIMEOUT` | `5m` | How long one publish may take, Claude Code's startup included |
| `CLAUDE_ARTIFACT_HELPER_MAX_BUDGET_USD` | `5` | Cap passed to `claude --max-budget-usd`, so a run that goes astray stops; `0` for none |
| `CLAUDE_ARTIFACT_HELPER_VERBOSE` | unset | `1` logs the command line and timing of each run on stderr |
| `CLAUDE_CODE_OAUTH_TOKEN` | unset | A token from `claude setup-token`, for a machine with no `claude auth login` |

## What the headless run can and cannot do

The run gets two tools, `Read` and `Artifact`, and a prompt that names the one file and the exact publish call. It cannot run commands, edit files, browse, or load skills or MCP servers, and it keeps no transcript. The file's content is sent to Anthropic by Claude Code under your login, as any `claude -p` run would send it; see [PRIVACY.md](PRIVACY.md). The artifact is private to your account until you share its link.

## License

MIT. See [LICENSE](LICENSE).
