import json
import unittest

from response import encode, restore_response


class ToolSecretTests(unittest.TestCase):
    handle = "[[secret:nvidia:123abc]]"
    canary = 'synthetic-"quoted"\\value'

    def completion(self):
        return {"choices": [{"index": 0, "message": {"role": "assistant",
                "content": self.handle, "reasoning_content": self.handle,
                "tool_calls": [{"id": "call-1", "type": "function", "function": {
                    "name": "bash", "arguments": encode({"command": self.handle}).decode()}}]},
                "finish_reason": "tool_calls"}]}

    def restore(self, value, **overrides):
        options = {"tool_secrets": {self.handle: self.canary}, "allowed_tools": {"bash"}}
        options.update(overrides)
        return restore_response(encode(value), lambda v: (v, 0), 1 << 20, **options)

    def test_secrets_are_restored_only_in_structured_tool_arguments(self):
        body, identities, restored_count = self.restore(self.completion())
        message = json.loads(body)["choices"][0]["message"]
        self.assertEqual(identities, 0)
        self.assertEqual(restored_count, 1)
        self.assertEqual(message["content"], self.handle)
        self.assertEqual(message["reasoning_content"], self.handle)
        self.assertEqual(json.loads(message["tool_calls"][0]["function"]["arguments"]), {"command": self.canary})

    def test_sse_and_legacy_function_arguments_keep_json_escaping(self):
        value = self.completion()
        message = value["choices"][0]["message"]
        message["function_call"] = message.pop("tool_calls")[0]["function"]
        body, _, restored_count = self.restore(value, stream=True)
        event = json.loads(body.splitlines()[0][6:])
        args = event["choices"][0]["delta"]["function_call"]["arguments"]
        self.assertEqual(json.loads(args), {"command": self.canary})
        self.assertEqual(restored_count, 1)

    def test_unparseable_tool_arguments_never_regain_secrets(self):
        for arguments in ['{"command": "' + self.handle, encode([self.handle]).decode()]:
            value = self.completion()
            value["choices"][0]["message"]["tool_calls"][0]["function"]["arguments"] = arguments
            with self.subTest(arguments=arguments):
                body, _, restored_count = self.restore(value)
                message = json.loads(body)["choices"][0]["message"]
                self.assertEqual(message["tool_calls"][0]["function"]["arguments"], arguments)
                self.assertEqual(restored_count, 0)

    def test_unknown_handle_or_unoffered_tool_blocks_delivery(self):
        for options in [{"tool_secrets": {}}, {"tool_secrets": {"[[secret:nvidia:000000]]": self.canary}},
                        {"allowed_tools": {"read"}}, {"allowed_tools": set()}]:
            with self.subTest(options=options), self.assertRaises(ValueError):
                self.restore(self.completion(), **options)

    def test_provider_errors_do_not_restore_tool_secrets(self):
        value = {"error": {"message": self.handle}}
        body, _, restored_count = self.restore(value)
        self.assertEqual(json.loads(body), value)
        self.assertEqual(restored_count, 0)

    def test_key_collision_and_expanded_body_are_refused(self):
        value = self.completion()
        function = value["choices"][0]["message"]["tool_calls"][0]["function"]
        function["arguments"] = encode({self.handle: 1, self.canary: 2}).decode()
        with self.assertRaises(ValueError):
            self.restore(value)
        with self.assertRaises(ValueError):
            self.restore(self.completion(), tool_secrets={self.handle: "x" * (1 << 20)})


if __name__ == "__main__":
    unittest.main()
