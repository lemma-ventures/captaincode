from itertools import product


def accept(state, generation, verified, fresh, previous_write, current_write):
    current, previous = state
    if not verified or not fresh:
        return state, "refused"
    if current is not None and generation <= current[0]:
        return state, "refused"
    candidate = (generation, generation + 100)
    next_previous = previous
    if current is not None:
        if not previous_write:
            return state, "write_failed"
        next_previous = current
    if not current_write:
        return (current, next_previous), "write_failed"
    return (candidate, next_previous), "accepted"


def rollback(state, previous_write, current_write):
    current, previous = state
    if current is None or previous is None:
        return state, "refused"
    if not previous_write:
        return state, "write_failed"
    if not current_write:
        return (current, current), "write_failed"
    return (previous, current), "rolled_back"


def restart(state, current_valid, previous_valid):
    current, previous = state
    if current is not None and not current_valid:
        return None
    if previous is not None and not previous_valid:
        return current, None
    return state


def valid_state(state):
    current, previous = state
    if current is not None and current[0] <= 0:
        return False
    if previous is not None and previous[0] <= 0:
        return False
    return True


def check_acceptance():
    checks = 0
    currents = (None, (1, 101), (2, 102))
    previous_records = (None, (1, 101), (2, 102), (3, 103))
    for current, previous in product(currents, previous_records):
        state = (current, previous)
        for generation, verified, fresh, previous_write, current_write in product(
            (1, 2, 3), (False, True), (False, True), (False, True), (False, True)
        ):
            after, outcome = accept(
                state, generation, verified, fresh, previous_write, current_write
            )
            assert valid_state(after)
            refused = not verified or not fresh or (
                current is not None and generation <= current[0]
            )
            if refused:
                assert outcome == "refused"
                assert after == state, "a refused envelope changes no record"
            elif outcome == "accepted":
                assert after[0][0] == generation
                assert current is None or generation > current[0]
                if current is None:
                    assert after[1] == previous
                else:
                    assert after[1] == current
            else:
                assert after[0] == current, "a failed current write keeps the accepted record"
            checks += 1
    return checks


def check_rollback():
    checks = 0
    states = (
        (None, None),
        ((1, 101), None),
        ((2, 102), (1, 101)),
        ((1, 101), (2, 102)),
    )
    for state, previous_write, current_write in product(states, (False, True), (False, True)):
        current, previous = state
        after, outcome = rollback(state, previous_write, current_write)
        assert valid_state(after)
        if current is None or previous is None:
            assert outcome == "refused" and after == state
        elif outcome == "rolled_back":
            assert after == (previous, current), "rollback swaps the two records"
            restored, restored_outcome = rollback(after, True, True)
            assert restored_outcome == "rolled_back" and restored == state
        else:
            assert after[0] == current, "a failed rollback keeps the current record"
        checks += 1
    return checks


def check_restart():
    checks = 0
    records = (None, (1, 101), (2, 102))
    for current, previous, current_valid, previous_valid in product(
        records, records, (False, True), (False, True)
    ):
        state = (current, previous)
        opened = restart(state, current_valid, previous_valid)
        if current is not None and not current_valid:
            assert opened is None, "a broken current record fails closed"
        else:
            assert opened is not None
            assert opened[0] == current
            if previous is not None and not previous_valid:
                assert opened[1] is None, "a broken rollback record is not restored"
        checks += 1
    return checks


def check():
    accepted = check_acceptance()
    rolled_back = check_rollback()
    restarted = check_restart()
    print(
        f"PASS: {accepted} acceptance transitions, {rolled_back} rollback transitions, "
        f"{restarted} restart states; single-writer sequential model"
    )


if __name__ == "__main__":
    check()
