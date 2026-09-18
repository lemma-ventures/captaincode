# Contributing

Captain Code is a tool we run daily and publish because it is useful. There is
no support contract, and the maintainers are a small team - a patch that is
small, tested and explained gets merged; a large one may sit.

## In scope

- New legs and runtimes (another CLI agent, another provider).
- Routing policy: better priors, better value ranking, better failure handling.
- Bug fixes, especially ones that come with a failing test.
- Documentation that stops the next person hitting what you hit.

## Out of scope for now

A hosted or multi-tenant service, a GUI, Windows support, and anything that
requires Captain Code to hold credentials of its own.

## How to contribute

1. Open an issue first for anything larger than a fix. Say what you are trying
   to do, not only what you want to change.
2. **Write the test first.** Every behavioural change in this repository lands
   with a test that failed before it. Watchdogs and routing in particular have
   two failure directions (fires when it should not; never fires) - test both.
3. `go test ./...` must be green. `gofmt` your code.
4. Sign off your commits: `git commit -s` adds the `Signed-off-by` line that
   certifies the [DCO](https://developercertificate.org/). We do not use a CLA.
5. Explain *why* in the commit message. The history here is the design record;
   a message that only restates the diff is a missed opportunity.

## Style

Comments explain the reason, not the mechanism - preferably the incident that
caused the line to exist. Follow what the surrounding file already does.

## Security

Do not report vulnerabilities in issues or pull requests; see
[SECURITY.md](SECURITY.md).
