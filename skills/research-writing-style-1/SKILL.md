---
name: research-writing-style-1
description: Draft, restructure or review security and cryptography research papers so a mixed audience understands them on first read - the service before the mechanism, assumptions attached to claims, evidence-specific verbs, and sentences that follow from one another. Use for abstracts, introductions, application-to-protocol explanations, security definitions, contribution narratives and author-guided revisions.
---

# Research writing: goals, assumptions, construction, evidence

A research paper makes an argument: a participant needs something, existing approaches leave part of it open, a construction answers it under stated assumptions, and evidence of a stated kind supports the answer. This skill keeps that argument visible and makes each sentence readable on the first pass. Write original prose. Read [the examples](references/examples.md) before revising an abstract, an overclaim, or prose for a mixed audience.

## Establish the argument before drafting

Identify the audience, venue constraints and requested deliverable from the task. Preserve text the user has frozen, and flag any unsupported claim in it separately. Do not silently repair a frozen abstract by making the body overclaim. Do not impose a previous project's page count or section plan on a new paper.

Privately map each central claim to:

- The question, and the existing approach that leaves it open.
- The service or outcome a named participant wants.
- The inputs, assumptions, adversarial capabilities and permitted leakage.
- The proposed mechanism and its actual implementation boundary.
- The supporting definition, theorem, experiment or explicit conjecture.
- The strongest inference a reader may make, and the main inference they may not.

If a necessary fact is missing, research it or mark the uncertainty. Do not invent an assumption, theorem, measurement or novelty claim to complete the narrative. This map is a drafting aid, not a deliverable.

## Select the entry point

**Application paper:** begin with the participant's task and a concrete failure of the current approach. Explain the workflow, state the required service, then introduce the abstraction and construction. Carry one example through the argument when it helps readers see a boundary. Keep human, institutional and legal assumptions visible beside the technical result.

**Protocol or foundations paper:** introduce the primitive and its intended use, identify the exact mismatch in an existing notion or assumption, then give the research question and its scoped answer. Present an informal result with its assumptions before detailed notation when possible. Explain the technical obstacle, construction idea, formal definition and proof strategy in dependency order.

**Mixed audience:** give a continuous prose argument that can be followed without equations, and put specialist definitions and proofs in a second layer. Keep any assumption essential to understanding the claim in the main text. Do not replace precise definitions with analogies.

These are adaptable argument structures, not compulsory tables of contents.

## Writing decisions

1. **Define the service before advertising the mechanism.** Say what a participant can submit, receive, verify or rely on. A list of algorithms is not a research goal.
2. **Make the gap specific and fair.** State what previous work achieves, under which assumptions, and which requirement remains unmet. Avoid unsupported claims that no solution or standard exists.
3. **Separate the goal, the construction and the evidence.** A desired service is not an implemented protocol; an implementation is not a security proof. Explain the connection that has actually been established.
4. **Name actors and observable behaviour.** Replace "the system guarantees security" with who learns what, who can change what, and what verification establishes. Include abort, delay, rejection and missing-output behaviour where it matters.
5. **Motivate definitions.** Introduce a term with one plain sentence on its role, then define it precisely where needed. Distinguish the paper's meaning from the everyday meaning of words such as "policy", "valid" or "audit".
6. **Explain the obstacle before the construction trick.** Show why an obvious approach fails or needs a stronger assumption. A counterexample often shows the difference better than adjectives.
7. **Keep assumptions attached to claims.** Write "under A, protocol P achieves G" only when supported. Do not claim composability because the architecture has modules.
8. **Use evidence-specific verbs.** "Define" introduces a model; "construct" gives a protocol; "prove" needs a mathematical argument; "implement" names executable work; "measure" describes experiments; "conjecture" marks an open claim. Do not flatten these into "demonstrate" or "show" unless the surrounding text makes the evidence clear.
9. **Give each paragraph a purpose:** establish a fact, expose a gap, explain a mechanism, or interpret evidence. Use "because", "therefore" and "however" only when that relation holds. Repeat a technical term rather than vary it with synonyms.
10. **Introduce notation when it becomes useful.** Explain the object in words, define its symbols, state the relation and its consequence. Do not open with a glossary.
11. **State contributions as checkable results:** the new object or finding, its conditions, its evidence and its significance. Implementation plumbing and section summaries are not contributions.
12. **Interpret cost and trade-offs.** Name the statement, baseline, workload, implementation version and conditions behind each measurement. Separate measured results, asymptotic bounds and extrapolations.
13. **Place limits where the reader needs them.** Attach a material restriction to the result it limits. Use the conclusion to explain what the results jointly establish and which obstacle remains, not to repeat disclaimers.

## Connected prose for application papers

- Introduce a participant through the decision it makes. Identify an oversight body by the decision it takes before describing what it needs to see. Do not define familiar words.
- After an example, refer to it and state the inference: "This example shows that…" followed by the specific requirement. Do not make a detail of the example an unexplained new subject. Avoid empty transitions such as "This illustrates the problem."
- Give a mechanism's purpose before its name or provenance. Put scheme attribution and configuration in the construction section unless it is needed to state the contribution.
- Make contributions continue the proposed answer. When the text enumerates challenges, separate the design responses from the implemented contributions and name who performs each response. Do not restart with a second ordinal list ("First, … Second, …") right after the design's own list; tie each contribution to its challenge and its evidence verb instead.
- Introduce a term where its referent first becomes useful, with its boundary. Do not append a detached glossary.
- Carry numbered challenges through prior work, design, evaluation and conclusion, in words as well as by number. State the partial result and the specific missing check or evidence for each.
- Start a technical section with the question it answers and how the previous result supplies its inputs. Labels and cross-references locate an explanation; they do not replace it.

## Write for first-read comprehension

A reader should understand each sentence once, left to right, without looking back or decoding. Long sentences are fine when they read straight through.

1. **Ground the problem in an actor acting on a concrete object.** Keep the detail that makes the situation picturable and that motivates the first challenge ("one operator stores the same backups in three regions"). Keep domain qualifiers the reader needs.
2. **Start each sentence from what the previous one named, and end it on what is new.** Recall a referent the reader already holds rather than coining a new summary noun. Every "the X" needs an antecedent; never open a sentence with a definite term the text has not introduced.
3. **Remove what forces re-reading:** cross-reference codes ("aim 1"), elliptical comparisons ("A with B, and C with D proofs"), modifiers whose scope is unclear ("expired key rotations, revocations and alerts": are the revocations expired too?), and runs of negatives. Rewrite rather than delete when the content is evidence.
4. **Use a causal verb only when the cause produces every listed effect.** Use "we" for the authors' own actions - propose, specify, implement, measure, prove - not for noticing problems.
5. **Number a list of challenges once in the abstract, and answer it in the same order in words.** In the body, carry the numbers and the words together.
6. **Order an abstract:** setting, problem, proposal tied back to the problem, status and material limits, then the strongest supported result last.
7. **Mark the answer as an answer** ("which addresses these challenges by…"), and give the status of anything not built ("is designed to", "we design"). Use the plain present tense only for implemented behaviour.
8. **Name the kind of result before its mechanics, with an evidence verb:** "We implement a prototype auditor that checks…", not "Here, we demonstrate…". Define the framing term first.
9. **Keep in the abstract any caveat that blocks the strongest wrong inference** - for example, that hiding inputs is not a proved zero-knowledge property, or that results assume the storage logs are complete. Merge it into the sentence that makes the claim. Move other caveats to the body, beside their claims.
10. **Never treat a result as a caveat.** Measurements and comparisons stay; rewrite them until they parse.
11. **State status plainly:** "designed but not yet built", never "requires further exploration". Cut filler ("in terms of", "the aspect of", "it should be noted that").
12. **Use serial commas** in lists of three or more.

## Revising someone else's draft

An author's revision teaches two different things. Learn their sentence-level preferences - order, transitions, register, what they cut as hard to parse - and apply them elsewhere. Do not learn their claim-boundary changes as style. When a revision does any of the following, keep the improved sentence shape and flag the change for the author to decide:

- a plain present tense for something not implemented;
- a result, measurement or comparison removed;
- an assumption or caveat removed that blocks a wrong inference;
- a status softened into a euphemism;
- a causal claim that covers more than the cause produces;
- a concrete detail replaced by an abstraction.

Preserve frozen text exactly and report discrepancies separately.

## Final review

Check that a reader can answer, in order:

- What problem matters, to whom, and why do existing approaches leave it open?
- What precisely is proposed, and how does it operate?
- What is assumed, exposed, excluded and allowed to fail?
- Which claims are proved, implemented, measured or still proposed?
- What does each result change, and what work would close the remaining gap?

Then read the text once at full speed. Mark every place you had to re-read or could not say who does what to what, and fix each mark. Check that every verb matches its evidence: proposed, implemented, measured or proved. Read only the paragraph openings in order: each must follow from the one before, and two neighbouring paragraphs must not both open an ordinal list. When revising a draft, list any result or assumption the revision dropped and ask before accepting the drop.

Repair missing links in the argument before polishing sentences. Check that abstract claims have matching evidence in the body and that the conclusion does not expand them. Deliver the requested draft or review; do not rewrite other files or publish it.
