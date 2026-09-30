import json

OPENROUTER = {
    "model": "openai/gpt-oss-120b", "host": "openrouter.ai", "base_path": "/api/v1",
    "key_env": "OPENROUTER_API_KEY", "file": "openrouter.yaml", "type": "captain-openrouter-pilot", "output": 16384,
}
LANES = {
    "cerebras": ("cerebras", "Cerebras"), "sambanova": ("sambanova", "SambaNova"),
    "together": ("together", "Together"), "deepinfra": ("deepinfra/bf16", "DeepInfra"),
    "crusoe": ("crusoe/bf16", "Crusoe"), "parasail": ("parasail/fp4", "Parasail"),
}
PROFILES = {
    "nim": {
        "model": "z-ai/glm-5.3-flash", "host": "integrate.api.nvidia.com", "base_path": "/v1",
        "key_env": "NVIDIA_API_KEY", "file": "nvidia.yaml", "type": "captain-nim-pilot", "output": 4096,
    },
    **{name: dict(OPENROUTER, route=route, served_by=served_by) for name, (route, served_by) in LANES.items()},
}


def profile(name):
    if name not in PROFILES:
        raise ValueError("unsupported inference profile")
    return dict(PROFILES[name])


def pin_request(body, name):
    selected = profile(name)
    if "route" in selected:
        if body.get("model") != selected["model"] or any(key in body for key in ("models", "route")):
            raise ValueError("request does not name the pinned model")
        body = dict(body, temperature=0, provider={
            "only": [selected["route"]], "order": [selected["route"]], "allow_fallbacks": False,
            "data_collection": "deny", "zdr": True,
        })
        maximum = body.get("max_tokens", selected["output"])
        if type(maximum) is not int or maximum < 1:
            raise ValueError("invalid token limit")
        if "max_completion_tokens" in body:
            raise ValueError("alternate token limit is not supported")
        body["max_tokens"] = min(maximum, selected["output"])
    return body


def canonical_calls(body):
    """Rename tool-call IDs to call_1, call_2... in order of first use.

    Workers and providers mint random IDs, so two runs with the same history
    would otherwise send different bytes from the second request on. Renaming
    is one-to-one within the request, so each result stays paired with its call.
    """
    if "messages" not in body:
        return body, 0
    if type(body["messages"]) is not list:
        raise ValueError("messages must be a list")
    names = {}

    def rename(value):
        if type(value) is not str:
            raise ValueError("tool call IDs must be strings")
        return names.setdefault(value, f"call_{len(names) + 1}")

    messages = []
    for message in body["messages"]:
        if type(message) is dict and message.get("tool_calls") is not None:
            calls = message["tool_calls"]
            if type(calls) is not list or any(type(call) is not dict for call in calls):
                raise ValueError("tool calls must be a list of objects")
            message = dict(message, tool_calls=[dict(call, id=rename(call["id"])) if "id" in call else call
                                                for call in calls])
        if type(message) is dict and "tool_call_id" in message:
            message = dict(message, tool_call_id=rename(message["tool_call_id"]))
        messages.append(message)
    return dict(body, messages=messages), len(names)


def response_provider(data, name, status):
    selected = profile(name)
    if "served_by" not in selected or not 200 <= status < 300:
        return None
    value = json.loads(data)
    if value.get("model") != selected["model"] or value.get("provider") != selected["served_by"]:
        raise ValueError("response does not confirm the pinned provider and model")
    return selected["served_by"]
