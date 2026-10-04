---
name: keep-private-names-private
description: Keep the names of private projects, repositories, papers, people and tools out of public repositories - code, comments, test fixtures, commit messages, docs, release notes and issues. Use whenever you write anything that could be committed, pushed or published, above all when you record a real incident, an example or a test fixture.
---

# Keep private names private

A public repository is permanent. Everything in it - every comment, test fixture, commit message and release note - can be copied, cached and indexed the moment it is pushed. Rewriting history afterwards removes it from the repository, but not from forks, mirrors, package proxies or anyone's clone. The only reliable fix is not to write the name in the first place.

## What counts as private

Anything that identifies work the owner has not published:

- names of internal projects, products, repositories and folders;
- titles and topics of unpublished papers, and the names of their authors or of the people whose work they study;
- names of internal tools, services, datasets, clients, partners and collaborators;
- local paths that reveal any of these (`~/work/<project>`, `/Users/<name>/…`);
- quotes or paraphrases of private documents.

When unsure, treat a name as private. A name is public only when the owner has published it - for example, the repository you are working in, its own documented features, and open-source dependencies.

## Write neutral names instead

Real incidents make good comments and tests; keep the incident, drop the identity.

| Instead of | Write |
|---|---|
| "a 26-minute run in <project> after a restart" | "a 26-minute run in a sibling repository after a restart" |
| a test folder named after a real repository | `repo-a`, `sibling`, a dictionary word when the test needs one (`oak`, `elm`) |
| "the routing test for the <paper title> paper" | "the routing test for a paper review" |
| "search through <internal tool>" | "search through the code-intelligence provider" |
| "<person>'s papers" | "the source papers" |

Keep the property a fixture tests: if a test needs a short lowercase word, use another short lowercase word; if it needs a mixed-case non-word, invent one.

## Before anything leaves the machine

1. **Write neutral from the start.** Comments, test names and fixtures, commit messages, docs and release notes.
2. **Check before you commit.** Where `captain` is installed, the owner keeps a private list outside every repository (`~/.config/captain/private-names`). Run `captain leakcheck --staged` before committing, `captain leakcheck <file>` on release notes and drafted issues, and `captain leakcheck --range origin/main..HEAD` before pushing. A repository's git hooks (`git config core.hooksPath .githooks`) run the same checks on every commit, commit message and push.
3. **Allowed phrases.** A public company or org name can contain a private project's name. The list marks it with `!` (for example `!acme-labs` beside a private `acme`), so the public name passes and the project name alone is still refused.
4. **Never add the list, or any name on it, to a repository.** A committed deny-list is itself the leak.
5. **If a check fires, rename - do not suppress.** Do not bypass a hook (`--no-verify`) to push a private name.
6. **If a name was already pushed, stop and tell the owner.** Removing it means rewriting the history of a public repository, which only the owner can decide.

## Final check

Read what you are about to commit or publish as a stranger would. Could they learn the name of something the owner has not published? If yes, rename it.
