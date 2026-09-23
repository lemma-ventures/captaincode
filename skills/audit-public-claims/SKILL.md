---
name: audit-public-claims
description: Check every factual claim in public text about software against what people will actually install or open, before it is published. Use when drafting or reviewing release notes, a changelog, a README, docs, a blog post, a launch or social post, or a reply to a user about how the software behaves; when a sentence says "now supports", "latest", "all", "every" or "always", or quotes a number; before cutting a release tag; and before deploying a website. Produces a claim, evidence and status table plus corrected wording.
license: MIT
---

# Audit public claims

Every sentence in public text that states a fact is a claim someone can check
against what they install or open. Audit each one before it ships. These rules
come from launching Captain Code; the incidents quoted below are its own.

## 1. Build the claim table

Pull out every checkable statement: features and behaviour ("X decides Y"),
supported versions, models and platforms, numbers and percentages,
comparisons, availability ("open source", "private", "free"), install
commands, links and anchors.

| # | Claim (verbatim) | Evidence (command, URL, date) | Status |
|---|---|---|---|

Status is exactly one of:

- **released**: true in a published release; name it (`v1.4.0`).
- **tree only**: true on main or in the working tree, in no release yet.
- **false**
- **unverified**: not checked, or cannot be checked.

Only **released** claims ship as written. A **tree only** claim waits for the
release or says so ("on main, not released yet"). A **false** claim is fixed
or cut. An **unverified** claim is checked or cut.

## 2. Check what users get, not what you have

- A change is released only if a tag contains it: `git tag --contains <commit>`.
  Read a file as released with `git show vX.Y.Z:path`; list a tag's files with
  `git archive vX.Y.Z | tar -t`.
- Check what the install command resolves to today: Go
  `go list -m <module>@latest`, npm `npm view <pkg> version`, PyPI
  `pip index versions <pkg>`, GitHub `gh release view --json tagName` (the
  release marked Latest).
- The binary on your PATH is not the process that is running. A daemon started
  before an upgrade serves the old code until it restarts; check both when a
  claim is about behaviour people will see.
- A reply about how the software behaves comes from reading the code path, not
  from a design doc or memory. A Captain reply explained how disagreements
  between parallel agents were settled, and described a step the code did not
  have yet. If it is not true yet, say "not yet", or build it and reply once it
  is released.

## 3. Numbers

- Recompute every number from raw data at publish time, and state the
  population, the date range and the method next to it.
- Do not let one period stand for the whole. Captain's share of runs on
  open-weight models was far higher in its first month than afterwards; a
  ">25%" written from that month was false for the whole history, which said
  about one run in six.
- A figure lives on in drafts and old pages. When it changes, grep everything
  published for the old one.

## 4. Universal and time-bound words

"latest", "all", "every", "always", "never", "only", "first", "full", "now":

- Each needs a live check at publish time against every case it covers, a date
  ("as of 23 September 2026"), or deletion.
- "Supports the latest models on every leg" failed Captain's own review because
  two vendors had shipped newer models than the ones the release pinned. Check
  each vendor's current model list, not your config.

## 5. Status changes

When anything changes status (private to public, beta to stable, paid to free,
a rename), grep every published page, draft and README for the old wording.
Captain's production blog kept serving an old draft that said the project
"remains private" after it was public.

## 6. Pages and deploys

- Every in-page link has a target: each `href="#x"` needs an element with
  `id="x"`. Trimming a section of Captain's features page left ten table rows
  linking to anchors that no longer existed.
- Generated files are rebuilt with their source. Fixing the markdown and
  shipping the old generated HTML publishes the old text.
- After a deploy, check staging and production separately; they drift.
- Link previews: `og:image` and `twitter:image` are absolute URLs that return
  200 with an image content type. Fetch them.
- Analytics: confirm events arrive from a real browser session. A privacy
  setting such as honouring Do Not Track can drop every event from your own
  browser, so "no events" may be the browser, not the deploy.
- A production deploy is outward-facing: confirm with the owner before
  triggering it. A Captain worker once committed, pushed and deployed analytics
  to production with neither a confirmation nor a browser check.

## 7. Scrub before anything goes public

- No absolute local paths, usernames, private repository names, internal
  hostnames, keys or tokens in docs, commit messages, tags or release notes.
  Scan the tree and the history you are about to publish:

  ```sh
  git grep -nIE '/Users/|/home/[a-z]|BEGIN [A-Z ]*PRIVATE KEY|(api|secret)[_-]?key' -- .
  git log --format=%B <range> | grep -nE '/Users/|/home/|Co-authored-by:|Session:'
  ```

- To publish a private repository, export a clean tree into a fresh history
  rather than flipping the old one public. The old history keeps everything
  ever committed to it.
- Attribution: if the project's policy is human-only authorship, read the final
  message of every commit before pushing (`git log -1 --format=%B`). Some agent
  tools append co-author or session trailers through hooks; if a hook re-adds
  them on amend, rebuild the commit with `git commit-tree`.

## 8. Output

1. The claim table.
2. For every claim that is not **released**: the corrected sentence (dated,
   scoped or reworded), or "cut".
3. What was checked and how (commands, URLs, dates), so anyone can rerun the
   audit.
