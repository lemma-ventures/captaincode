# The Captain command language

Version **1.0** · normative · implementation: `pkg/captaincode/language.go`

Every line typed into a Captain session is a program in this language: a task, plus the words
that say who runs it, how, how often, and in what order. This document defines the language.
Where the code and this document disagree, the disagreement is a bug, and the conformance suite
(§10) exists to catch it.

**Who reads it.** The brain parses every line deterministically, with no model call:
the same line always gets the same reading. The director, a model, never interprets syntax.
The brain only calls it to do work a construct asks for: planning a `/team`, compiling `/wf`
English into a workflow, and reviewing a workflow's final stage. To see how a line is read without
sending it, run:

```
captain parse "/frontier write the specs > /repeat 5 /quality implement them"
(chain (frontier "write the specs") (repeat 5 (auto [quality] "implement them")))
```

Contents: [1 Design rules](#1-design-rules) · [2 Lexical structure](#2-lexical-structure) ·
[3 Grammar](#3-grammar) · [4 Disambiguation](#4-disambiguation) ·
[5 Order of reading](#5-order-of-reading) · [6 Meaning](#6-meaning) ·
[7 Limits and errors](#7-limits-and-errors) · [8 Safety](#8-safety) ·
[9 Reserved words](#9-reserved-words) · [10 Maintaining the language](#10-maintaining-the-language)

## 1. Design rules

Every construct in the language, and every change to it, keeps these rules:

1. **Prose stays prose.** A connector or group counts only when a command follows it. A line
   that does not use the syntax means what it says, so adding syntax never changes the meaning
   of plain text.
2. **No silent rewriting.** A line that looks like syntax but breaks a rule is reported, never
   truncated or quietly run as something else.
3. **Modifiers compose in any order.** `/oss /repeat 5 x` and `/repeat 5 /oss x` mean the same.
4. **Bounded work.** Every line runs a bounded number of turns (§8), and only a person can start
   a loop.
5. **Deterministic reading.** The reading of a line depends only on the line and the registered
   legs; no model is consulted.

## 2. Lexical structure

A line is read as a sequence of tokens. Case is ignored for every word below.

| Token | Form | Notes |
|---|---|---|
| slash word | `/` letter { letter, digit, `-` } | A word only when not followed by `/`: `/quality/report.md` and `/usr/bin/claude` are paths, i.e. text. A word may be followed by `:` (`/grok: fix it`). |
| count | digit { digit } | Only directly after `/repeat`. |
| sequence connector | `>`, `->`, or the word `then` | Symbols may touch their neighbours; `then` needs spaces around it. |
| parallel connector | `+`, `&`, the word `and`, or a newline | Workflows only (§6.4). |
| group | `(` … `)` | `(` opens a group only when a slash word follows it (spaces allowed between). |
| gate marker | `gate:` | The last `gate:` in a workflow stage; everything after it is a shell command. |
| text | anything else | Words, paths, punctuation, numbers. |

A **blank line** (two newlines with only spaces between) ends the language's reach: a connector
whose preceding text contains a blank line is text. Pasted documents that quote syntax therefore
stay text.

## 3. Grammar

EBNF (ISO 14977 style). Terminals are quoted; `{ x }` is zero or more, `[ x ]` is optional.
The grammar alone is ambiguous; §4 resolves every ambiguity, and §5 gives the order in which
readings are tried.

```ebnf
line          = control-line | sequence ;

(* §6.7: a word that takes the rest of the line as its argument *)
control-line  = "/" control-word , rest-of-line ;
control-word  = "btw" | "interrupt" | "init" | "context" | "euclid" | "private"
              | "captain" | "help" | "wf" | "workflow" | "run" | "wfrun" | "rename" ;

(* §6.5 chains and §6.4 workflows share the sequence connector *)
sequence      = element , { SEQ , element } ;
element       = group | stage ;
group         = "(" , sequence , ")" ;
stage         = command , { PAR , command } ;          (* PAR only between leg commands *)

command       = { modifier } , [ head ] , { modifier } , [ body ] , [ gate ] ;
head          = leg
              | "/frontier" | "/team" | "/auto" | "/openshell"
              | "/repeat" , [ count ] , [ repeat-control ]
              | "/parallel" , [ "show" | "status" | "stop" ] ;
body          = text | group ;                          (* a group only after /repeat *)
gate          = "gate:" , shell-command ;
repeat-control = ( "status" | "show" | "watch" | "stop" | "finish" | "wrapup" | "abort" ) ,
                 [ "last" | "all" | thread-id ] ;
thread-id     = ( "rp_" | "ch_" ) , hex-digits ;

modifier      = lane | pool | skill | "/frontier" ;
lane          = "/quality" | "/q" | "/best" | "/speed" | "/fast" | "/save" | "/cheap" ;
pool          = "/oss" | "/open" | "/deterministic" | "/det" | "/adi" ;
skill         = "/noslop" ;
leg           = "/" , leg-id ;                          (* the registered legs: `captain legs` *)

SEQ           = ">" | "->" | "then" ;
PAR           = "+" | "&" | "and" | newline ;
```

## 4. Disambiguation

These rules decide between the readings the grammar allows. Each is tested in the conformance
suite.

1. **Connector rule.** `>`/`->` count as a connector only when followed by a slash word that
   names a leg, `/frontier`, `/team`, `/repeat`, `/auto`, a modifier, or a group. `+`/`&` count
   only before a leg. The word connectors `then`, `and` and a newline count only before a leg
   that can run a task, and `then` sequences workflow stages only: chains use `>` or `->`.
   Anywhere else they are text: `check that x > y` is a task. In a line led by a leg, a `>`
   before an unknown slash word is an error rather than text (§7), so a mistyped leg is never
   silently dropped.
2. **Group rule.** `(` opens a group only before a slash word. A group is atomic: a connector
   inside it belongs to the group. `/repeat 3 (/a x > /b y)` is one `/repeat` whose body is a
   chain.
3. **Workflow or chain.** A sequence whose elements are all leg stages (legs, `/frontier`, with
   `+` inside a stage) is a **workflow** (§6.4). A sequence with any other element (`/team`,
   `/repeat`, a lane, a group, …) is a **chain** (§6.5). A single element is a **command**.
4. **Control lines own their line.** After `/btw`, `/wf`, `/private` and the other control words,
   nothing is syntax: `/wf save review /grok a > /claude b` saves that expression.
5. **OpenShell owns its line.** A line led by `/openshell`, or a `/team` that names `/openshell`,
   runs entirely in the sandbox, unless the line is a chain. `/openshell scan > /claude fix` runs
   in the sandbox; `/openshell scan > /repeat 2 /quality fix` is a chain.
6. **Modifiers before a head move behind it.** `/oss /repeat 5 x` reads as `/repeat 5 /oss x`.
   `/frontier` is a modifier when a leg or control word follows it (`/frontier /codex x` runs codex
   at its ceiling), and a head otherwise.
7. **Modifiers anywhere apply to the turn.** A lane, pool or skill word preceded by a space
   anywhere in the text applies to the whole turn. The first lane wins. A `/frontier` anywhere
   but the head overrides the lane. The words stay in the text the worker receives.
8. **`/repeat` control words.** `/repeat` followed by exactly one control word, optionally plus
   `last`, `all` or a thread id, manages loops. Anything wordier is a task to repeat:
   `/repeat watch the queue` repeats "watch the queue". A bare `/repeat` is `status`.
9. **A bare leg inherits.** In a workflow, a leg with no text takes the previous stage's text
   (`/grok review X > /codex`). Within a stage, legs with no text take the stage's first text.

## 5. Order of reading

The brain tries readings in this order. The first that matches wins. `ParseTurn` follows the
same order.

1. **Refusal (TUI).** A line whose first slash word is unknown is refused before it is sent,
   with the nearest known words.
2. **Hoisting.** Leading modifiers move behind the head (§4.6).
3. **Chain.** A sequence with a non-leg element (§4.3).
4. **OpenShell.** A line led by `/openshell`, or `/team` naming it (§4.5).
5. **Workflow.** A leg sequence with two or more stages, or a gate.
6. **`/frontier`, `/team`.**
7. **Control words and loops.** `/private`, `/captain`/`/help`, `/context`, `/euclid`,
   `/parallel`, `/repeat`, `/btw`, `/interrupt`, `/init`.
8. **Workflow commands.** `/wf`, `/workflow`, `/run`, `/wfrun`.
9. **Malformed workflow.** A line that looks like a workflow (a leg head and a connector before
   a slash word) but fails to parse is reported as an error.
10. **A forced leg, or routing.** A leg head runs that leg. Anything else is routed: triage, the
    director's pick, a worker, an assessment ([ROUTING.md](ROUTING.md)).

## 6. Meaning

### 6.1 A task

Text with no head is routed ([ROUTING.md](ROUTING.md)). `/auto` asks for routing explicitly.

### 6.2 Heads

| Head | Meaning |
|---|---|
| `/<leg>` | That leg runs the turn ([LEGS_AND_TIERS.md](LEGS_AND_TIERS.md)). |
| `/frontier` | The frontier lane at maximum effort; the frontier legs take turns. |
| `/team` | The director plans an ensemble of up to 3 workers and reviews their outputs into one answer. A lane or a leg after it binds the ensemble. |
| `/openshell` | The task runs in an OpenShell sandbox ([CONFIGURATION.md](CONFIGURATION.md#openshell-workers)). |
| `/parallel` | The task runs beside the chat. `/parallel show`, `status`, `stop` manage it. |

### 6.3 Modifiers

| Word | Kind | Meaning |
|---|---|---|
| `/quality` `/q` `/best` | lane | The best legs by blended quality, at high effort. |
| `/speed` `/fast` | lane | The director picks among fast legs. |
| `/save` `/cheap` | lane | The open-weight legs that clear the quality bar, at medium effort. |
| `/frontier` | effort | The leg's most capable settings (as a modifier, §4.6). |
| `/oss` `/open` | pool | Open-weight models only. |
| `/deterministic` `/det` `/adi` | pool | Legs green in the Agentic Determinism Index only ([ADI.md](ADI.md)). |
| `/noslop` | skill | The plain-writing rules for every worker of the turn. |

### 6.4 Workflows (Captain Workflow Language)

Legs in stages. `>` runs stages in sequence; `+` runs legs of a stage in parallel. Each stage
sees the outputs of the stage before it. The director reviews the final stage and returns one
answer. A `gate:` makes a stage count only when its command exits 0, after one repair retry.
Design and rationale: [WORKFLOW_LANGUAGE.md](WORKFLOW_LANGUAGE.md).

### 6.5 Chains

Whole commands in sequence. Each step runs as a full turn, through this same order of reading,
after the previous step finishes. It sees the conversation before the chain, the chain as typed,
and the end of the previous step's answer. A failed step ends the chain. A group step that holds
a chain runs as a nested chain; one that holds a single command or a workflow runs as that.
Chains run as background threads (ids `ch_…`) under the `/repeat` control words.

### 6.6 Loops

`/repeat N body` runs the body N times, each round a full turn after the last. Without N it runs
until stopped, up to `CAPTAIN_REPEAT_MAX` rounds (100). A loop also stops when two rounds report
the same thing, after three failures in a row, or when the run budget is spent (§8). The control
words:

| Form | Meaning |
|---|---|
| `/repeat` · `/repeat status` | The loops and chains in this folder |
| `/repeat show [id]` · `/repeat watch [id]` | What recent rounds did · follow one live |
| `/repeat stop` · `finish` · `wrapup` `[id\|all]` | End after the current round or step |
| `/repeat abort [id\|all]` | End now; the round in flight is lost |

### 6.7 Control lines

| Word | Meaning |
|---|---|
| `/btw <note>` | Tell the running worker more. |
| `/interrupt [reason]` | Stop the running worker and keep its work as a handoff. |
| `/wf <english>` (`/workflow`) | Compile English into a workflow and preview it. `/wf save <name> <expression>`, `/wf run <name>`, `/wf list` keep named ones. |
| `/run <wf_id>` (`/wfrun`) | Run a compiled workflow. |
| `/captain …` · `/help` | Steer the director and the routing mix; `/captain` alone prints the cheat sheet ([TUI.md](TUI.md#steer-the-director)). |
| `/init` | Regenerate worker permissions. |
| `/context …` | Cross-project context sharing ([TUI.md](TUI.md#settings--memory)). |
| `/euclid status` · `/euclid distill [apply]` | Euclid memory ([EUCLID.md](EUCLID.md)). |
| `/private [add\|dismiss\|remove <names>]` | The private-names list. |
| `/rename <title>` | Rename the session (handled by the TUI). |

## 7. Limits and errors

| Limit | Value | Past it |
|---|---|---|
| Workflow stages | 4 | Error |
| Legs per workflow stage | 4 | Error |
| Worker runs per workflow | 8 | Error |
| Chain steps | 8 | Not read as a chain |
| Group nesting | 3 levels | Not read as a chain |
| `/repeat` rounds | `CAPTAIN_REPEAT_MAX` (100) | The loop ends |
| Turns per typed line, loops and chains included | `CAPTAIN_RUN_BUDGET` (200) | The thread ends and says so |
| `captain send` prompts between two typed turns | `CAPTAIN_INBOX_QUOTA` (5) | Refused (HTTP 429) |

| Error | Cause |
|---|---|
| `/x is not a command - nothing was sent. Did you mean …?` | Unknown first slash word (TUI) |
| `unknown leg /x` | A workflow names a leg that is not registered |
| `/quality is a preference prefix, not a leg` | A lane joined to a workflow stage with `+` (`/grok a + /quality b`); in sequence, `/grok a > /quality b` is a chain |
| `stage N must start with a leg prefix` | A workflow stage without a head |
| `a sent prompt may not start a /repeat or a chain` | `captain send` with a loop (HTTP 403) |

## 8. Safety

Proved in Lean 4 in [formal/](../formal/README.md), and checked in CI:

- the chain splitter drops and invents no text, never turns prose into structure, and always
  terminates on nested groups;
- the turns one typed line can start, through any nesting of loops and chains, never exceed
  the run budget;
- a prompt sent with `captain send` never starts a loop, and at most the inbox quota of them is
  accepted between two turns the user types.

A change that adds a way to start turns (a new loop, a new re-entry path) must extend the model
and its proofs in the same change (§10.2).

## 9. Reserved words

Every slash word in the table is reserved. A new leg may not take one of these names, and
`captain legs add` refuses them.

| Words | Role |
|---|---|
| `/auto` `/frontier` `/team` `/openshell` | Heads |
| `/repeat` `/parallel` | Loop and background heads |
| `/quality` `/q` `/best` `/speed` `/fast` `/save` `/cheap` | Lanes |
| `/oss` `/open` `/deterministic` `/det` `/adi` | Pools |
| `/noslop` | Skill |
| `/btw` `/interrupt` `/init` `/context` `/euclid` `/private` `/captain` `/help` `/rename` | Control lines |
| `/wf` `/workflow` `/run` `/wfrun` | Workflow commands |

`/repeat` reserves its control words: `status`, `show`, `watch`, `stop`, `finish`, `wrapup`,
`abort`. Each registered leg id is a word too (`captain legs`).

## 10. Maintaining the language

### 10.1 Versions

The language version is `LanguageVersion` in `pkg/captaincode/language.go` and the version at
the top of this document; a test keeps them equal. Versioning is semantic, judged by readings:

- **Major.** Any line that was valid before is read differently. Avoid; when unavoidable, keep
  the old reading available for one minor version with a warning.
- **Minor.** A new word, form or limit that leaves every existing reading unchanged.
- **Patch.** A fix that makes the code match this document.

### 10.2 Changing the language

A change to the language is one commit (or one pull request) that contains all of:

1. **This document**: the grammar (§3), the rule that resolves any new ambiguity (§4), the
   order of reading if it moves (§5), the meaning (§6), limits and errors (§7), and reserved
   words (§9).
2. **The parser**: the code in `pkg/captaincode` that the brain dispatches on, and `ParseTurn`
   if the order of reading changed.
3. **Conformance cases** in `pkg/captaincode/testdata/language/conformance.txt`: the new form,
   the prose that must stay prose next to it, and the error it can produce.
4. **The formal model**, when the change adds a way to start turns or re-enter the brain
   (§8).
5. **The cheat sheets**: [TUI.md](TUI.md) and the `/captain` text in
   `cmd/captaincode/brain_help.go`.
6. **The version bump**, and a line in the changelog (§10.5).

Before proposing a form, check it against the design rules (§1). In particular, run every
existing conformance case: a new form must not change any of their trees.

### 10.3 Deprecation

A word or form being removed keeps working for at least one minor version and prints which
form replaces it. The changelog names the version that removes it.

### 10.4 Conformance

`go test ./pkg/captaincode -run Language` runs:

- `TestLanguageConformance`: every case in the suite parses to its recorded tree;
- `TestLanguageEveryReservedWordIsDocumented`: every reserved word appears in this document;
- `TestLanguageVersionMatchesTheSpec`: this document's version equals the code's.

An expected tree is never edited only to make a test pass. If a reading changes, this document
says why and the version moves.

### 10.5 Changelog

| Version | Date | Change |
|---|---|---|
| 1.0 | 2026-10-05 | First specification of the whole line: workflows (CWL, 2026-07-30), modifiers and hoisting, chains and groups, `/repeat` control words with chain ids, the run budget and the inbox rules. Lines led by `/openshell` that are chains now run as chains. |
