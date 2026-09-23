# Skills

Three Agent Skills distilled from building and running Captain Code. Each is a
folder with a `SKILL.md`: plain procedures that do not need Captain installed
and run no scripts.

| Skill | Load it when |
|---|---|
| [`land-parallel-agent-work`](land-parallel-agent-work/SKILL.md) | several agents edit one repository at once and their changes have to land |
| [`verify-with-verdicts`](verify-with-verdicts/SKILL.md) | deciding whether a change works: pass, fail or inconclusive |
| [`audit-public-claims`](audit-public-claims/SKILL.md) | release notes, docs, posts or replies state facts about software |

## Install

Per repository. Codex, Gemini CLI, opencode and Cursor read `.agents/skills/`;
Claude Code reads `.claude/skills/`, where a symlink into `.agents/skills/`
works:

```sh
mkdir -p .agents/skills .claude/skills
cp -R <captaincode checkout>/skills/verify-with-verdicts .agents/skills/
ln -s ../../.agents/skills/verify-with-verdicts .claude/skills/verify-with-verdicts
```

For every repository on the machine, copy the folder to
`~/.claude/skills/<name>` for Claude Code or `~/.agents/skills/<name>` for
Codex.

Each runtime lists a skill by its name and description and reads the body only
when a task calls for it, so an installed skill costs one line of startup
context until it is used.
