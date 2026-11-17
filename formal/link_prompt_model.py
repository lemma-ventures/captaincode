from itertools import product


def admit(state, verified, linked, single_turn, replayed):
    if state in ("accepted", "refused"):
        return state
    if not verified or not linked or not single_turn or replayed:
        return "refused"
    return "accepted"


def headless(state, mode, keyboard_open):
    return state == "accepted" and mode == "active" and not keyboard_open


def tool_allowed(tool):
    return tool in ("read", "grep", "glob", "git_status", "git_log")


def check():
    transitions = 0
    for initial, verified, linked, single_turn, replayed, mode, keyboard in product(
        ("pending", "accepted", "refused"),
        (False, True),
        (False, True),
        (False, True),
        (False, True),
        ("active", "queue"),
        (False, True),
    ):
        state = admit(initial, verified, linked, single_turn, replayed)
        if initial != "pending":
            assert state == initial
        elif not verified or not linked or not single_turn or replayed:
            assert state == "refused"
            assert not headless(state, mode, keyboard)
        else:
            assert state == "accepted"
            assert headless(state, mode, keyboard) == (mode == "active" and not keyboard)
        assert admit(state, verified, linked, single_turn, replayed) == state
        transitions += 1
    for tool in ("read", "grep", "glob", "git_status", "git_log", "write", "edit", "test", "network", "repeat", "team"):
        assert tool_allowed(tool) == (tool in ("read", "grep", "glob", "git_status", "git_log"))
        transitions += 1
    print(f"PASS: {transitions} prompt and tool transitions; replay and queue-only execution refused")


if __name__ == "__main__":
    check()
