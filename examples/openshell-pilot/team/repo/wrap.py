def wrap(text, width):
    lines = []
    line = ""
    for word in text.split(" "):
        if len(line) + len(word) > width:
            lines.append(line)
            line = word
        else:
            line = line + " " + word if line else word
    lines.append(line)
    return lines
