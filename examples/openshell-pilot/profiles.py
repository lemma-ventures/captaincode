import json

PROFILES = {
    "nim": {
        "model": "z-ai/glm-5.3-flash", "host": "integrate.api.nvidia.com",
        "base_path": "/v1", "key_env": "NVIDIA_API_KEY", "file": "nvidia.yaml", "output": 4096,
    },
    "cerebras": {
        "model": "openai/gpt-oss-120b", "host": "openrouter.ai",
        "base_path": "/api/v1", "key_env": "OPENROUTER_API_KEY", "file": "openrouter.yaml", "output": 16384,
    },
}


def profile(name):
    if name not in PROFILES:
        raise ValueError("unsupported inference profile")
    return dict(PROFILES[name])


def pin_request(body, name):
    if name == "cerebras":
        if body.get("model") != profile(name)["model"] or any(key in body for key in ("models", "route")):
            raise ValueError("request does not name the pinned model")
        body = dict(body, temperature=0, provider={
            "only": ["cerebras"], "order": ["cerebras"], "allow_fallbacks": False,
            "data_collection": "deny", "zdr": True,
        })
        maximum = body.get("max_tokens", profile(name)["output"])
        if type(maximum) is not int or maximum < 1:
            raise ValueError("invalid token limit")
        if "max_completion_tokens" in body:
            raise ValueError("alternate token limit is not supported")
        body["max_tokens"] = min(maximum, profile(name)["output"])
    return body


def response_provider(data, name, status):
    if name != "cerebras" or not 200 <= status < 300:
        return None
    value = json.loads(data)
    if value.get("model") != profile(name)["model"] or value.get("provider") != "Cerebras":
        raise ValueError("response does not confirm the pinned provider and model")
    return "Cerebras"
