# Privacy

claude-artifact-helper collects no data of its own. It has no telemetry or analytics, and the only network
traffic it causes is Claude Code's: each publish runs `claude -p` on this machine, which sends the file you
named, and the short prompt around it, to Anthropic under your own Claude login and Anthropic's terms. The
artifact it creates lives on claude.ai in your account, private until you share its link.

Nothing is stored on disk by this tool. The headless run is started without session persistence, so Claude
Code keeps no transcript of it either.

Questions go to [the issue tracker](https://github.com/tyrantkhan/claude-artifact-helper/issues).
