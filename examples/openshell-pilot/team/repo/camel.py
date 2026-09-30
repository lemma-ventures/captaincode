def to_snake(name):
    out = ""
    for ch in name:
        if ch.isupper():
            out += "_" + ch.lower()
        else:
            out += ch
    return out
