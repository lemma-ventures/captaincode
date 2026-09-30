import json


def encode(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()


def functions(value):
    for choice in value.get("choices", []):
        message = choice["message"]
        if message.get("function_call"):
            yield message["function_call"]
        for call in message.get("tool_calls") or []:
            yield call["function"]


def restore_response(body, restore, limit, stream=False):
    if len(body) > limit:
        raise ValueError("response exceeds limit")
    value = json.loads(body)
    if not isinstance(value, dict):
        raise TypeError("expected JSON response")
    for function in functions(value):
        if function.get("arguments"):
            arguments = json.loads(function["arguments"])
            if not isinstance(arguments, dict):
                raise TypeError("tool arguments must be an object")
            function["arguments"] = arguments
    restored, count = restore(value)
    for function in functions(restored):
        if isinstance(function.get("arguments"), dict):
            function["arguments"] = encode(function["arguments"]).decode()
    output = completion_events(restored) if stream else encode(restored)
    if len(output) > limit:
        raise ValueError("restored response exceeds limit")
    return output, count


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
