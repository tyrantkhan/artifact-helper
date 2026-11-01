# Privacy

claude-artifact-helper collects no data of its own. It has no telemetry or analytics, and the only network
traffic it causes is Claude Code's: each command runs `claude -p` on this machine, which sends the short prompt,
the file you publish, and whatever the Artifact tool returns (a page, a comment listing) to Anthropic under
your own Claude login and Anthropic's terms. The artifacts live on claude.ai in your account, private until
you share their links.

The only files this tool writes are the ones you ask for: a fetched page lands in the directory you name, or a
temporary one. The headless runs are started without session persistence, so Claude Code keeps no transcript
of them either.

Questions go to [the issue tracker](https://github.com/tyrantkhan/claude-artifact-helper/issues).
