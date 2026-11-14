from itertools import product


def accept(used, quota, verified, nonce):
    if not verified or nonce in used:
        return used, quota
    return used | {nonce}, 0


def authorize(high, decision):
    return not high or decision


# A signed prompt may carry a prepare action: it targets a discovered project
# (a repository under an allowed root) instead of a linked folder. The prompt's
# admission is a receipt (LM-16): the same nonce with the same content returns
# its state; the same nonce with different content is refused.

PROJECT_STATES = ("discovered", "initializing", "ready", "setup_failed")


def admit(admissions, nonce, project, content):
    a = admissions.get(nonce)
    if a is None:
        return {**admissions, nonce: (project, content, "initializing")}
    if a[0] != project or a[1] != content:
        return None
    return admissions


def prepare(state, ok):
    if state == "ready":
        return "ready"
    if state == "setup_failed":
        return "setup_failed"
    return "ready" if ok else "setup_failed"


def check_prepare():
    checks = 0
    nonces = ("n1", "n2")
    for bits in range(16):
        base = {n: ("p1", "c1", "ready") for n in nonces if bits & (1 << nonces.index(n))}
        for nonce, project, content in (
            ("n1", "p1", "c1"),
            ("n1", "p1", "c2"),
            ("n1", "p2", "c1"),
            ("n2", "p1", "c1"),
            ("n3", "p1", "c1"),
        ):
            out = admit(base, nonce, project, content)
            a = base.get(nonce)
            if a is not None and (a[0] != project or a[1] != content):
                assert out is None, "changed content must be refused"
            else:
                assert out is not None, "same content must return a state"
                got = out[nonce]
                if a is None:
                    assert got == (project, content, "initializing")
                else:
                    assert got == a, "a repeat must not change the receipt"
                    assert admit(out, nonce, project, content) is out
            checks += 1
    for state in PROJECT_STATES:
        for ok in (False, True):
            after = prepare(state, ok)
            assert after in PROJECT_STATES
            if state == "ready":
                assert after == "ready", "a ready project stays ready"
            if state == "setup_failed":
                assert after == "setup_failed", "a failed setup needs a new prompt"
            if state in ("discovered", "initializing"):
                assert after == ("ready" if ok else "setup_failed")
            assert prepare(after, True) == after or after == "setup_failed"
            checks += 1
    print(f"PASS: {checks} prepare transitions; {len(PROJECT_STATES)} project states, 3 nonce classes")


def check():
    checks = 0
    for bits, quota, verified, nonce in product(range(16), range(6), (False, True), range(4)):
        used = frozenset(n for n in range(4) if bits & (1 << n))
        after = accept(used, quota, verified, nonce)
        if not verified or nonce in used:
            assert after == (used, quota)
        else:
            assert after == (used | {nonce}, 0)
            assert accept(*after, True, nonce) == after
        for high, decision in product((False, True), repeat=2):
            assert authorize(high, decision) == (not high or decision)
            if high and not decision:
                assert not authorize(high, decision)
        checks += 1
    print(f"PASS: {checks} exhaustive transitions; 4 nonce classes, 6 quota states")
    check_prepare()


if __name__ == "__main__":
    check()
