def human_bytes(size):
    for unit in ["B", "KiB", "MiB", "GiB"]:
        if size < 1024:
            return f"{size} {unit}"
        size /= 1024
