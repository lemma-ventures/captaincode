# Decisions for the user

Captain appends to this file. A worker adds an entry when it posts a question
that only the user can answer. Captain adds an entry when the user answers the
question or the worker withdraws it. The answer itself goes in the decision
record that the entry names. Do not edit old entries. After a merge conflict,
keep the entries from both sides.

## dq-261115-c32ef1 answered: Rule export contract for authenticated snapshots

- Question: BB3 is blocked on the rule export and authority contract. Which path should the next build take?
- Answered: 2026-11-16
- Choice: a: Narrow sender contract - Write down the exact export fields first, then build one small exporter around the pinned upstream rule code and prove it with rejection tests.
