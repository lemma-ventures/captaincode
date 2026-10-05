# Other orchestrators as Captain programs

Status: analysis, 2026-10-05. No importer exists. The examples on this page parse with the
program parser (`TestDocumentedProgramsParse` checks them); the framework snippets are short
sketches, not runnable files.

Captain programs ([WORKFLOW_LANGUAGE.md §11](WORKFLOW_LANGUAGE.md#11-programs-groups-chains-loops-and-fallbacks-level-2))
express most of the control flow that agent frameworks use: sequence, fan-out and join, loops
with an exit, a branch on failure, a manager that delegates, and subgraphs. This page maps
LangGraph, CrewAI, AutoGen, the OpenAI Agents SDK and Mastra onto Captain commands. It also
lists what does not map, and how an importer would work.

## The one difference that matters

In these frameworks a node is **code**: a function or an agent that reads and writes a typed
state object. In Captain a node is a **turn**: a prompt that a coding agent runs in your
repository, for minutes, with its own tools and its own inner loop. The shared state is the
repository and its git history, plus the end of the previous answer.

So the **topology** compiles and the node code does not. An import needs, for each node, a leg
(or `/team`) and a prompt. A conditional edge compiles only when it is a loop's exit or a branch
on failure.

## Concept map

| Concept | LangGraph | CrewAI | AutoGen AgentChat | OpenAI Agents SDK | Mastra | Captain |
|---|---|---|---|---|---|---|
| Sequence | `add_edge(a, b)` | `Process.sequential`, task `context` | `DiGraphBuilder.add_edge` | a handoff | `.then()` | `>` |
| Fan-out, then join | parallel edges | `async_execution=True` + `context` | parallel edges into one node | `asyncio.gather` over runs | `.parallel()` | `+` (4 legs per stage, 8 workers per workflow) |
| One branch per item | `Send` | - | - | - | `.foreach()` | no fixed form; `/team` lets the director choose up to 4 workers |
| Loop with an exit | a cycle with a conditional edge | a Flow `@router` | a conditional back edge | the agent loop | `.dountil()`, `.dowhile()` | `/repeat N … until: <command>` |
| Branch on failure | a conditional edge on an error | task `guardrail` with retries | a conditional edge | a guardrail tripwire | `.branch()` | `\|\|`, and `gate:` with one repair |
| Branch on content | `add_conditional_edges`, `Command(goto=…)` | `@router` | `add_edge(…, condition=…)`, `SelectorGroupChat` | the model picks a handoff | `.branch()` | the director: a bare prompt, or `/team` |
| A manager delegates | supervisor pattern | `Process.hierarchical` with `manager_llm` | `SelectorGroupChat` | agents as tools (`as_tool`) | - | `/team` |
| Subgraph | a compiled graph as a node | a crew inside a Flow | a team inside a team | `as_tool` | a workflow as a step | `( … )` |
| Output check | a check node and an edge | `guardrail`, `guardrail_max_retries` | `TextMentionTermination` | `output_guardrails` | a step's output schema | `gate: <command>`: an exit code, one repair |
| Step budget | `recursion_limit` | per-agent iteration limits | `MaxMessageTermination` | `max_turns` (default 10) | - | `/repeat N`, and 100 turns per program |
| Human approval | `interrupt()` with a checkpointer | - | `UserProxyAgent` | - | suspend and resume | end the program there; `/wf` previews; `/btw` and `/interrupt` steer a running worker |

LangGraph's default `recursion_limit` depends on the release (25 in older releases and in the
SDK, 1000 since 1.0.6), so set it explicitly when you compare budgets.

## LangGraph patterns, compiled

### Reflection: generate, review, repeat until approved

```python
g = StateGraph(State)
g.add_node("generate", generate)
g.add_node("reflect", reflect)
g.add_edge(START, "generate")
g.add_edge("generate", "reflect")
g.add_conditional_edges("reflect", lambda s: END if s["approved"] else "generate")
```

The exit in LangGraph is a model's verdict in the state. Captain's exit is an exit code, so use
a check that does not depend on what a model says, or a fixed number of rounds:

```captain
/repeat 6 (/codex write the parser > /claude review the parser and list what to fix) until: go test ./parser/...
/repeat 3 (/codex write the parser > /claude review the parser and list what to fix)
```

### Plan and execute

A planner writes steps, an executor does one step per pass, a replanner decides whether to go
on. In Captain the plan is a file in the repository, and the exit check reads it:

```captain
/frontier write the plan as a checklist in PLAN.md > /repeat 8 /codex do the next unchecked item in PLAN.md and tick it gate: go test ./... until: ! grep -q '\[ \]' PLAN.md
```

### Map-reduce with `Send`

A fixed fan-out compiles. A fan-out whose width depends on the data does not: name at most 4
legs, or let the director choose with `/team`.

```captain
/codex review pkg/auth + /grok review pkg/billing + /claude review pkg/api > /frontier merge the three reviews into one report
/team review every package under pkg/ and merge the findings into one report
```

### Supervisor

A supervisor routes to the next worker until the work is done. That is the director's job: one
`/team` turn per pass, and an objective exit.

```captain
/repeat 5 /team migrate the next service to the new client until: make test
```

### An error branch

```captain
/codex apply the schema migration gate: make migrate-check || /claude explain why the migration fails and propose a fix
```

### Human approval (`interrupt()`)

A program cannot pause for input and resume. Split it where the approval sits: run the first
part, read the result, then type the second part. For a plan written in English, `/wf` compiles
it and shows it before anything runs.

## CrewAI, AutoGen, OpenAI Agents SDK, Mastra

- **CrewAI.** A sequential crew whose tasks pass `context` is a chain:
  `/grok research the market > /claude write the brief`. A hierarchical crew with a
  `manager_llm` is `/team`. A task guardrail with retries is `gate:`, with one difference:
  Captain judges an exit code, CrewAI judges a function's verdict. A Flow `@router` that branches
  on content has no static form.
- **AutoGen AgentChat.** A `GraphFlow` maps like a LangGraph graph. A round-robin team with
  `MaxMessageTermination(n) | TextMentionTermination("APPROVE")` is a bounded loop over a chain;
  replace the text trigger with a check that has an exit code.
- **OpenAI Agents SDK.** A fixed handoff is `>`. Agents used as tools (`as_tool`) are `/team`.
  `output_guardrails` are `gate:`. `max_turns` is the round count and the turn budget.
- **Mastra.** `.then()` is `>`, `.parallel()` is `+`, `.dountil(step, cond)` is
  `/repeat … until: <cond>`, and `.dowhile(step, cond)` is `/repeat … until: ! <cond>`.
  `.branch()` on content and `.foreach()` have no static form.

## What does not compile

1. **Branches on content or state**: a router function, `Command(goto=…)`, `@router`,
   `.branch()`. A program branches only on failure. Routing on what a task says is the
   director's job.
2. **Fan-out whose width depends on the data**: `Send` per item, `.foreach()`. A stage runs at
   most 4 legs and a workflow at most 8 workers.
3. **Typed shared state with reducers.** A step reads the end of earlier answers (8,000
   characters, 8 steps back) and the repository.
4. **Parallel branches of several steps each.** They would write one checkout at the same time.
   Captain refuses `(A > B) + (C > D)`.
5. **Pause for a human, then resume** inside one program.
6. **Step-for-step cost parity.** A LangGraph node is one function call; a Captain turn is a
   whole agent run. A graph that takes 25 supersteps is not 25 Captain turns, and a budget
   copied across is wrong in both directions.

## How an importer would work (proposal)

`captain wf import <graph.json> --map <nodes.yaml>`, not built:

1. Read a graph's topology: LangGraph's `get_graph().to_json()` (nodes, edges, and which edges
   are conditional), or an AutoGen `GraphFlow` or a Mastra workflow definition.
2. Read a map: for each node, a leg (or `team`) and a prompt; for each loop, a round count and an
   exit command.
3. Collapse each cycle that has exactly one exit into `/repeat N ( … ) until: <command>`.
4. Keep conditional edges only when they are a loop's exit or a failure branch (`||`). Refuse the
   rest, and name the edge.
5. Layer the remaining graph into stages. Refuse a stage wider than 4 legs, and a graph that is
   not a series-parallel graph; name the node that breaks it.
6. Print the program and its `/wf parse` plan. Never run it: the user sends it.
