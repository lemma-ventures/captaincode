---
name: public-clarity-output
description: Workers format output in ASD-STE100 simplified English so their reports are as scannable as their code.
license: MIT
---

# public-clarity-output

This skill controls how workers, directors and assessors format **text output** that a human reads — handoff briefs, director rulings, grading notes, progress reports and `/btw` replies. It does not control the prompt that reaches a model, or the conversation the user types.

## Writing rules (1–2 sentence rule, then examples)

Write every sentence in **simplified technical English** inspired by ASD-STE100 (aerospace maintenance documentation standard). The goal is scannability, not formality: short sentences, one topic per sentence, consistent terms, no ambiguity.

| Do | Do not |
|---|---|
| "The patch applies cleanly. All 4 fixture tests pass." | "The patch has been successfully applied to the repository and our comprehensive suite of 4 fixture tests are executing without any errors whatsoever." |
| "Edge case: `/dev/null` input returns an empty string. The code handles this." | "Edge cases such as when the input is `/dev/null` cause it to return an empty string as one might expect, which the implementation does indeed account for." |
| "Two changes conflict in `auth.go`. The director lands `worker-1` — equivalent patches, lowest id." | "After careful evaluation by our director Claude, it has been determined that the work performed by worker-1 and worker-2 both address the same code areas and are logically equivalent, with the decision being made to proceed with worker-1 based on standard arbitration protocol." |
| "Objective added to `obj-det/constants.py`, lines 42–57. Four types: Sink, Source, Pipe, Filter. Build passes, `test_dag` passes." | "We introduced a new objective system, which can be found defined in the file `obj-det/constants.py` — specifically, lines 42 through 57 — containing four distinct types being Sink, Source, Pipe, and Filter; all existing build and DAG tests pass with this change." |
| "Verdict: good (8/10). All checks pass. Minor: the client retry loop has no backoff — a 10ms delay would soften a string of 429s." | "The overall quality of this submission is good, earning a score of 8 out of 10. All automated checks pass successfully. There is one minor concern — the client retry loop currently lacks exponential backoff, which would degrade performance during periods of high load when the API returns 429 responses." |

## When a diagram, HTML page or video would help

If all three are true, **offer the richer format first** instead of only text:

1. The concept is hard to explain in 3 sentences — a data flow, a state machine, a dependency cycle, a sequence of events across threads or processes.
2. You can produce it without an API call or an outside tool — SVG inline, HTML with inline CSS/JS, or Mermaid in a markdown block.
3. The reader would need that explanation to decide whether the work is correct.

When you produce one, also give the 3-sentence text version so the reader can scan.

## Rules for directors and assessors

The constraint prompt (`directorConstraint`) already says "your reply must be the JSON object and NOTHING else." That is the envelope. The content inside that JSON — `reason`, `notes`, `rationale`, `brief` — follows the rules above.

- **Ruling `reason`:** ≤ 140 chars, one sentence. "Worker A produced a passing patch. Worker B's test failed. Land A." Not "Both workers produced patches but upon evaluating the results worker B's test did not pass while worker A's did so landing A."
- **Assessment `notes`:** ≤ 140 chars. Start with the verdict. "All checks pass. Minor: no retry backoff on 429 — add 10ms delay." Not "The tests all pass and the implementation looks correct however one small improvement might be to add a backoff."
- **Planner `rationale`:** ≤ 140 chars. One sentence: the split, or why no split. "Parser and handler are independent — one stage, two parallel workers." Not "Due to the independent nature of the parser modifications and the handler changes it makes sense to run these in parallel in a single stage."
- **Worker `brief`:** ≤ 140 chars per stage spec. Phrase as an instruction. "Add retry to HTTP client in `fetch.go`. Max 3 attempts, 100ms backoff." Not "Please implement a retry mechanism in the HTTP client module located at `fetch.go` with a maximum of 3 retry attempts and 100 millisecond base backoff."
