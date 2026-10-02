UNITS = {"h": 3600, "m": 60, "s": 1}


def parse_duration(text):
    return int(text[:-1]) * UNITS[text[-1]]
