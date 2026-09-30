def encode(text):
    out = ""
    count = 1
    for i in range(1, len(text)):
        if text[i] == text[i - 1]:
            count += 1
        else:
            out += f"{count}{text[i - 1]}"
            count = 1
    return out


def decode(data):
    out = ""
    count = ""
    for ch in data:
        if ch.isdigit():
            count += ch
        else:
            out += ch * int(count)
            count = ""
    return out
