import json
import re

HANDLE = re.compile(r"\[\[secret:[a-z\-]+:[0-9a-f]{6}\]\]")


def encode(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()


def functions(value):
    for choice in value.get("choices", []):
        message = choice["message"]
        if message.get("function_call"):
            yield message["function_call"]
        for call in message.get("tool_calls") or []:
            if call.get("type") != "function":
                raise ValueError("unsupported tool call type")
            yield call["function"]


def response_usage(body):
    """The provider's own token counts and bill for one whole response: numbers, never content."""
    try:
        value = json.loads(body)
    except (ValueError, RecursionError):
        return None
    usage = value.get("usage") if isinstance(value, dict) else None
    if not isinstance(usage, dict):
        return None
    details = usage.get("completion_tokens_details")
    counts = {"prompt_tokens": usage.get("prompt_tokens"), "completion_tokens": usage.get("completion_tokens"),
              "reasoning_tokens": details.get("reasoning_tokens") if isinstance(details, dict) else None}
    found = {key: n for key, n in counts.items() if type(n) is int and 0 <= n < 1 << 32}
    cost = usage.get("cost")
    # A range check, not math.isfinite: it also refuses NaN and infinity, and
    # isfinite raises OverflowError on a huge integer.
    if type(cost) in (int, float) and 0 <= cost < 1000:
        found["cost"] = float(cost)
    return found or None


def committed_spend(rows):
    """USD a strict-capped Shield has committed, from its audit rows: each
    forwarded request's bill once a response settled it, else its whole
    reservation. Rows pair by reservation token, never by request ID."""
    reserved, settled, breached = {}, {}, False
    for row in rows:
        token = row.get("reservation")
        if token is not None and "reserved_usd" in row:
            reserved[token] = row["reserved_usd"]
        elif token is not None and "settled_usd" in row:
            settled[token] = row["settled_usd"]
        breached = breached or bool(row.get("budget_breached"))
    if any(type(value) not in (int, float) or not 0 <= value < 1000
           for value in [*reserved.values(), *settled.values()]) or settled.keys() - reserved.keys():
        raise ValueError("invalid budget audit")
    return sum(settled.get(token, value) for token, value in reserved.items()), breached


def restore_response(body, restore, limit, stream=False, tool_secrets=None, allowed_tools=None):
    if len(body) > limit:
        raise ValueError("response exceeds limit")
    value = json.loads(body)
    if not isinstance(value, dict):
        raise TypeError("expected JSON response")
    for function in functions(value):
        if function.get("arguments"):
            try:
                arguments = json.loads(function["arguments"])
            except ValueError:
                continue
            if isinstance(arguments, dict):
                function["arguments"] = arguments
    restored, count = restore(value)
    restored_count = 0
    expanded_bytes = 0

    def replace(match):
        nonlocal restored_count, expanded_bytes
        if match[0] not in tool_secrets:
            raise ValueError("tool secret was not masked in this request")
        restored_count += 1
        expanded_bytes += len(tool_secrets[match[0]].encode())
        if expanded_bytes > limit:
            raise ValueError("restored tool secrets exceed limit")
        return tool_secrets[match[0]]

    def walk(item):
        if isinstance(item, str):
            return HANDLE.sub(replace, item)
        if isinstance(item, list):
            return [walk(child) for child in item]
        if isinstance(item, dict):
            output = {}
            for key, child in item.items():
                key = walk(key)
                if key in output:
                    raise ValueError("restored tool keys collide")
                output[key] = walk(child)
            return output
        return item

    for function in functions(restored):
        if isinstance(function.get("arguments"), dict):
            if tool_secrets is not None and HANDLE.search(encode(function["arguments"]).decode()):
                if function.get("name") not in (allowed_tools or set()):
                    raise ValueError("tool was not offered in this request")
                function["arguments"] = walk(function["arguments"])
            function["arguments"] = encode(function["arguments"]).decode()
    output = completion_events(restored) if stream else encode(restored)
    if len(output) > limit:
        raise ValueError("restored response exceeds limit")
    return output, count, restored_count


def completion_events(value):
    if not isinstance(value.get("choices"), list) or not value["choices"]:
        raise ValueError("missing completion choices")
    chunks, endings, seen = [], [], set()
    for choice in value["choices"]:
        index = choice["index"]
        if type(index) is not int or not 0 <= index < 128 or index in seen:
            raise ValueError("invalid choice index")
        seen.add(index)
        message = choice["message"]
        if not isinstance(message, dict) or message.get("role") != "assistant":
            raise ValueError("invalid completion message")
        if not isinstance(choice.get("finish_reason"), str):
            raise TypeError("incomplete completion")
        delta = dict(message)
        if message.get("tool_calls") is not None:
            delta["tool_calls"] = [dict(call, index=i) for i, call in enumerate(message["tool_calls"])]
        chunks.append(dict(choice, delta=delta, finish_reason=None))
        endings.append(dict(choice, delta={}, logprobs=None))
        del chunks[-1]["message"]
        del endings[-1]["message"]
    base = {key: value[key] for key in ("id", "created", "model", "system_fingerprint", "service_tier") if key in value}
    base["object"] = "chat.completion.chunk"
    events = [dict(base, choices=chunks), dict(base, choices=endings)]
    if value.get("usage") is not None:
        events.append(dict(base, choices=[], usage=value["usage"]))
    return b"".join(b"data: " + encode(event) + b"\n\n" for event in events) + b"data: [DONE]\n\n"
