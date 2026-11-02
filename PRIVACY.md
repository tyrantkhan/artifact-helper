# Privacy

artifact-helper collects no data of its own. It has no telemetry or analytics, and the only network
traffic it causes is Claude Code's. Each command runs `claude -p` on this machine, which sends to Anthropic,
under your own Claude login and Anthropic's terms, what any `claude -p` run would send: the short prompt, the
file you publish, whatever the Artifact tool returns (a page, a comment listing), and the context Claude Code
loads for a run, such as CLAUDE.md files in and above the folder it starts in. A run that publishes starts in
the published file's folder, and a read with `--out` in a new folder inside the one it names; every other run
starts in an empty temporary folder. The artifacts live on
claude.ai in your account, private until you share their links.

These are the files the tool writes:

- A fetched page, in the folder you name with `--out`, or a temporary one.
- The setup `init` saves: `config.json` in your user config folder, holding the organization's name and ID
  and the Claude Code config folder to use.
- The skill stub `install` writes to Claude Code's skills folder, and a backup of a copy it replaces with
  `--force`, in a folder beside it.
- With `init --plugin`, Claude Code itself adds this repository as a plugin marketplace and installs the
  plugin, in its own folder.

The headless runs are started without session persistence, so Claude Code keeps no transcript of them.

Questions go to [the issue tracker](https://github.com/tyrantkhan/artifact-helper/issues).
