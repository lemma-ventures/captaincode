from itertools import product


def accept(used, quota, verified, nonce):
    if not verified or nonce in used:
        return used, quota
    return used | {nonce}, 0


def authorize(high, decision):
    return not high or decision


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


if __name__ == "__main__":
    check()
