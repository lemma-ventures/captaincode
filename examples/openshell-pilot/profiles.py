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
# A strict dollar cap prices each lane at a ceiling, in USD per million prompt
# and completion tokens. Shield sends it as OpenRouter's max_price, so a lane
# that costs more is refused before it generates, and reserves against it.
# List prices on 2 October 2026: Cerebras 0.35/0.75, SambaNova 0.14/0.95,
# Together 0.15/0.60, DeepInfra 0.037/0.17, Crusoe 0.05/0.25, Parasail 0.10/0.75.
CEILINGS = {
    "cerebras": (0.45, 0.95), "sambanova": (0.18, 1.20), "together": (0.19, 0.75),
    "deepinfra": (0.05, 0.22), "crusoe": (0.07, 0.32), "parasail": (0.13, 0.95),
}
# Template tokens a provider adds around the forwarded messages; gpt-oss adds
# about 70. Byte-level tokenizers emit at most one token per body byte.
PROMPT_MARGIN = 4096
PROFILES = {
    "nim": {
        "model": "z-ai/glm-5.3-flash", "host": "integrate.api.nvidia.com", "base_path": "/v1",
        "key_env": "NVIDIA_API_KEY", "file": "nvidia.yaml", "type": "captain-nim-pilot", "output": 4096,
    },
    **{name: dict(OPENROUTER, route=route, served_by=served_by, ceiling=CEILINGS[name])
       for name, (route, served_by) in LANES.items()},
}


def profile(name):
    if name not in PROFILES:
        raise ValueError("unsupported inference profile")
    return dict(PROFILES[name])


def pin_request(body, name, capped=False):
    selected = profile(name)
    if capped and "ceiling" not in selected:
        raise ValueError("a strict cost cap needs a priced lane")
    if "route" in selected:
        if body.get("model") != selected["model"] or any(key in body for key in ("models", "route")):
            raise ValueError("request does not name the pinned model")
        provider = {
            "only": [selected["route"]], "order": [selected["route"]], "allow_fallbacks": False,
            "data_collection": "deny", "zdr": True,
        }
        if capped:
            # No per-request fee: the reservation prices tokens only.
            provider["max_price"] = {"prompt": selected["ceiling"][0], "completion": selected["ceiling"][1], "request": 0}
        body = dict(body, temperature=0, provider=provider)
        maximum = body.get("max_tokens", selected["output"])
        if type(maximum) is not int or maximum < 1:
            raise ValueError("invalid token limit")
        if "max_completion_tokens" in body:
            raise ValueError("alternate token limit is not supported")
        body["max_tokens"] = min(maximum, selected["output"])
    return body


def reservation(body, max_tokens, name):
    """The most one forwarded request can cost, in USD: prompt tokens bounded by
    the body's bytes plus the template margin, completion tokens by the clamped
    max_tokens, both at the lane's ceilings."""
    prompt, completion = profile(name)["ceiling"]
    return ((len(body) + PROMPT_MARGIN) * prompt + max_tokens * completion) / 1_000_000


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
