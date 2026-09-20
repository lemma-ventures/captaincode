# The Captain Code plugins for stock opencode

Captain Code does not fork [opencode](https://opencode.ai). Everything the
terminal needs is two plugins that stock opencode loads from its own config:

| File | Registered in | What it does |
|---|---|---|
| `captain.ts` | `~/.config/opencode/opencode.jsonc` | forced leg prefixes (`/claude …`, `/team …`, `/oss …`), the leg roster from the brain, the route log, and the secret masking at the tool boundary |
| `captain-ui/index.tsx` | `~/.config/opencode/tui.json` | the Models sidebar, the wordmark, the director tag and the progress feed; ships a `captain-demo` OpenCode theme (website demo colors) selectable via **Captain chrome…** or `CAPTAIN_UI_CHROME=demo` |

The two halves must be separate modules and they go in **different** config
files: opencode.jsonc feeds the server plugin loader, tui.json feeds the TUI.
A UI plugin listed in opencode.jsonc is silently never loaded - the panels just
do not appear. opencode also refuses a single module that exports both
`server()` and `tui()`.

## Install

`captain init` writes both entries, as absolute `file://` URLs pointing at this
directory, so a GUI-launched opencode finds them without a shell PATH or a
useful cwd:

```sh
git clone https://github.com/lemma-ventures/captaincode
cd captaincode
go build -o ~/.local/bin/captain ./cmd/captaincode
captain init
```

The panels have their own dependencies (`solid-js`, `@opentui/*`). Install them
once, from this directory:

```sh
cd plugin/captain-ui && bun install
```

Then launch with `./captaincode.sh`, or with `opencode` directly.

`captain init` warns when it cannot find these sources - a `go install` of the
brain alone has no checkout, so it has no plugins to register and the terminal
runs unrouted.

## What no plugin slot reaches

Two fork behaviours have no public slot, and the plugins do not replace them:
the "Continue captaincode -s …" presentation string, and the team/subagent
footer labels. Neither affects routing.

Director routing for unprefixed prompts is deliberately **not** done here:
`chat.message` runs before the message renders, so deciding in the plugin froze
the UI for the length of a director call. Unprefixed prompts go to
`captain/auto` and the brain routes inside the turn instead.
