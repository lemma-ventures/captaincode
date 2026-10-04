# Examples and review cases

Every example here is invented for instruction. The running case is a storage provider that must prove to its customers that their backups are still held and can be recovered. Numbers and system names are fictional; never cite them as measurements.

## Application: explain the service and its limit

Weak:

> Our protocol guarantees verifiable data durability through cryptographic storage proofs.

Better:

> A storage provider keeps backups for its customers. A customer needs to know, without downloading everything, whether the provider still holds every file it was paid to keep. The proposed protocol lets the provider answer a random challenge with a short proof computed from the stored data. The proof says nothing about whether the files can be restored within the contract's deadline, or whether the provider keeps copies it should have deleted.

Why: it names both participants, states the question, describes the service, and keeps two obligations the proof does not cover in view.

## Foundations: motivate a definition

Weak:

> We introduce a novel strong notion of retrievability.

Better:

> A provider can pass a single audit by keeping only the blocks it expects to be asked about. We therefore ask for a stronger property: anyone who answers audits correctly with high probability can be used to reconstruct the whole file. We then ask how many challenged blocks per audit suffice for that reconstruction.

Why: it shows the failure first, then the property that rules it out, then the question. It claims no construction and no novelty.

## Results: keep each inference within its evidence

Weak:

> The prototype proves retention in milliseconds and keeps customers' data private.

Better, for a hypothetical study:

> In this experiment, answering one audit over a 10 GB file took a median of 40 ms on one server. The experiment did not include restoring data from cold storage. A separate test found no file contents in the audit responses; that test does not establish a privacy property.

Why: it names the measured object and conditions, keeps an untested path out of the claim, and separates an observation from a theorem.

## Construction: explain why a restriction is needed

Weak:

> Let T be the tier map and impose all constraints on T.

Better:

> A file kept only in cold storage can take hours to retrieve, so it cannot meet a one-hour restore deadline. The scheduling rule therefore first decides which storage tiers may serve each deadline. The formal relation records these permitted pairs as edges between tiers and deadlines.

Why: it explains the purpose of the modelling choice before its notation. The exact tier rule still has to be specified.

## Draw the inference from an example

Weak:

> The provider stores 1,000 files; 50 are in cold storage with a six-hour retrieval delay. The audit must respect tiers and deadlines.

Better:

> This example shows that a one-hour audit must count 950 recoverable files, not 1,000. If the 50 files are later moved to warm storage, the audit record must still keep the earlier shortfall.

Why: each requirement follows from a specific number in the example, so the reader sees why the rule exists.

## Explain a component's job before its name

Weak:

> Our second contribution is an audit circuit for backend X, an implementation of scheme Y.

Better:

> A customer must be able to check the audit result without rerunning it on the provider's full storage index. We encode the tier and deadline checks in a proof circuit. The construction section names the proof backend and the assumptions needed to trust its output.

Why: the reader first learns why the component exists. The backend is still named, where the construction is introduced.

## Name a process once it has been explained

Weak:

> Continuous auditing denotes the intended service.

Better:

> Each new audit record refers to the previous one and keeps its recorded shortfalls. We call this ongoing process continuous auditing; the prototype audits once a day, so it says nothing about the hours between audits.

Why: the term names a process the reader has just seen, and its boundary stays attached to it.

## Contributions after a design list

Weak:

> The design works as follows. First, the provider maps contracts into one retention state. Second, it chains audit records. Third, it schedules recovery proofs.
>
> We make three contributions. First, we specify an audit model. …

Better:

> For repeated audits (1), the provider maps every contract onto one retention state. For deletions between audits (2), it chains audit records so a gap stays visible. For cold-storage delays (3), it schedules recovery proofs by tier.
>
> This paper develops the responses to challenges (2) and (3): chained records and tier-aware scheduling. We specify an audit model for them. We then implement a proof circuit for the chain and tier checks, and we measure its cost against signed logs.

Why: two neighbouring paragraphs no longer both open with "First,". Each contribution answers a named challenge with its own evidence verb.

## An abstract revised for first-read comprehension

### The draft

> Abstract. Backup providers face customer-specific requirements for retention and recoverability. An operator storing data in Frankfurt, Dublin and Virginia may audit the same backups under three customer contracts. We identify four challenges: (1) repeated audits per contract; (2) spot checks that miss data deleted and restored between audits; (3) recovery estimates that ignore cold-storage delays; and (4) audit reports that reveal other customers' metadata. We propose TALLY, a retention protocol designed to (1) map contracts into one retention state; (2) chain audit records to expose deletions between checks; (3) schedule recovery proofs using storage tiers and retrieval delays; and (4) give each customer only its own results. A prototype checks retention proofs for one tier per day. Contract mapping (aim 1) and per-customer results (aim 4) remain unimplemented. Benchmarks compare TALLY with signed logs, and full with sampled proofs. Privacy and end-to-end security are unproved. Results assume complete storage logs.

It reads as a ledger. "We identify" puts the authors between the reader and the problem. The second list repeats the numbers, "(aim 1)" sends the reader back to decode a code, and "and full with sampled proofs" is elliptical. The abstract ends on a run of negatives instead of its result.

### An author's revision

> Backup providers face customer-specific requirements for retention and recoverability. An operator storing data across Frankfurt, Dublin, and Virginia is subjected to differing contractual obligations, including those set out in each customer's agreement. This patchwork leads to recurring audit challenges: (1) repeated audits per contract; (2) spot checks that miss data deleted and restored between audits; (3) recovery estimates that ignore cold-storage delays; and (4) audit reports that reveal other customers' metadata. We propose TALLY, which addresses these challenges through a retention protocol that maps contracts into one retention state; chains audit records to expose deletions between checks; schedules recovery proofs using storage tiers and retrieval delays; and gives each customer only its own results. The proof system is experimental. Contract mapping and per-customer results require further exploration. Here, we show a prototype core where a checker verifies retention proofs for one tier per day.

**What reads better, and should be learned:**
- Sentences pick up what the reader already holds ("This patchwork", "these challenges").
- The challenges are numbered once. The answer follows in the same order, as verbs, with no codes.
- "which addresses these challenges" marks the answer as an answer.
- The result moves to the end, so the abstract closes on something built.
- The elliptical benchmark sentence and the run of negatives are gone.

**What changed the claims, and should be flagged rather than learned:**
- "maps … gives each customer only its own results" is now plain present tense, but contract mapping and per-customer results are not built.
- The privacy caveat is gone, so "only its own results" invites a privacy reading nobody has proved.
- The benchmark comparison, which was a result, was deleted instead of rewritten.
- "Results assume complete storage logs" was dropped, though it changes what the result means.
- "require further exploration" blurs "not yet built".
- "This patchwork leads to" all four challenges, but cold-storage delays (3) arise even with one contract.
- "the same backups", the detail that made challenge (1) picturable, became the abstract "differing contractual obligations".
- "The proof system" appears before any proof has been mentioned.
- "Here, we show a prototype core where" uses a weak verb and an undefined frame.

### Applying the skill

> Backup providers must show each customer that its data is retained and recoverable. An operator that keeps the same backups in Frankfurt, Dublin, and Virginia audits them once for every customer contract, which repeats the same work (1). Each audit also leaves three gaps: (2) spot checks miss data deleted and restored between audits; (3) recovery estimates ignore delays in retrieving data from cold storage; and (4) audit reports reveal other customers' metadata. We propose TALLY, a retention protocol designed to close these gaps: it maps every contract onto one retention state, chains audit records so a deletion between checks stays visible, schedules recovery proofs by storage tier and retrieval delay, and gives each customer only its own results. Contract mapping and per-customer results are designed but not yet built, and withholding other customers' metadata is not a proved privacy property. We implement a prototype auditor that checks retention proofs for one storage tier per day; assuming complete storage logs, it detects deletions between audits that signed logs miss, at the cost of a larger daily proof.

Why: it keeps every improvement in the author's revision and restores what the revision dropped: the status of unbuilt parts, the caveat that blocks the privacy reading, the measured comparison and the log assumption. Each sentence starts from what the last one gave, and the abstract still ends on its result.

## Review cases

Use these as behavioural checks when revising this skill. Judge the reasoning and the claim boundaries, not exact wording.

1. **Mixed-audience rewrite.** Input: an acronym-heavy paragraph claiming a successful proof means contractual compliance. Expected: name the participants and the verified computation; keep contract interpretation as a separate obligation; keep supported results. Failure: shorter sentences with the same inference.
2. **Protocol result.** Input: a logarithmic proof size, linear verification and a trusted setup. Expected: all three facts stay in the result. Failure: "an efficient proof system with no setup".
3. **Frozen abstract.** Input: an abstract the author has frozen, which claims composability no theorem supports. Expected: keep it exactly, flag the gap separately, and write a body limited to the evidence. Failure: invent a theorem or silently edit the abstract.
4. **Narrow request.** Input: improve one paragraph. Expected: apply the relevant rules to that paragraph only. Failure: restructure the paper or edit other files.
5. **Missing evidence.** Input: a protocol with no implementation. Expected: describe the construction and the outstanding evaluation honestly. Failure: supply a plausible benchmark.
6. **Author revision.** Input: an author's rewrite that reads better but drops a caveat and states an unbuilt part in the present tense. Expected: keep the new sentence shapes, restore the status and the caveat, and list the drops for the author. Failure: copy the claim changes as style, or reject the whole revision.
7. **Two ordinal lists.** Input: a design paragraph and a contributions paragraph that both start "First,". Expected: tie each design response and each contribution to its challenge. Failure: rename "First" to "Firstly".
