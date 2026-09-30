import json
import unittest

from response import encode, restore_response


class ResponseTests(unittest.TestCase):
    def restore(self, value):
        count = 0

        def walk(item):
            nonlocal count
            if isinstance(item, str):
                count += item.count("identity-1")
                return item.replace("identity-1", 'Pilot "Person" 7d84')
            if isinstance(item, list):
                return [walk(child) for child in item]
            if isinstance(item, dict):
                return {key: walk(child) for key, child in item.items()}
            return item

        return walk(value), count

    def completion(self):
        return {"id": "chat-1", "object": "chat.completion", "created": 123, "model": "test-model",
                "choices": [{"index": 0, "message": {"role": "assistant", "content": "identity-1",
                             "reasoning_content": "identity-1"}, "finish_reason": "stop"}],
                "usage": {"total_tokens": 9007199254740993}}

    def events(self, value):
        output, count = restore_response(encode(value), self.restore, 1 << 20, stream=True)
        self.assertTrue(output.endswith(b"data: [DONE]\n\n"))
        return [json.loads(line[6:]) for line in output.splitlines()
                if line.startswith(b"data: ") and line != b"data: [DONE]"], count

    def test_complete_response_becomes_content_finish_usage_and_done_events(self):
        rows, count = self.events(self.completion())
        self.assertEqual(count, 2)
        self.assertEqual(len(rows), 3)
        self.assertEqual(rows[0]["object"], "chat.completion.chunk")
        self.assertEqual(rows[0]["choices"][0]["delta"]["content"], 'Pilot "Person" 7d84')
        self.assertEqual(rows[0]["choices"][0]["delta"]["reasoning_content"], 'Pilot "Person" 7d84')
        self.assertIsNone(rows[0]["choices"][0]["finish_reason"])
        self.assertEqual(rows[1]["choices"][0]["finish_reason"], "stop")
        self.assertEqual(rows[1]["choices"][0]["delta"], {})
        self.assertEqual(rows[2]["usage"]["total_tokens"], 9007199254740993)

    def test_multiple_choices_and_tools_keep_identity_and_argument_escaping(self):
        value = self.completion()
        calls = [{"id": "call-1", "type": "function", "function": {"name": "read", "arguments":
                  '{"path":"identity-1","secret":"[[secret:nvidia:123456]]"}'}},
                 {"id": "call-2", "type": "function", "function": {"name": "read", "arguments": "{}"}}]
        value["choices"][0]["message"]["tool_calls"] = calls
        value["choices"][0]["finish_reason"] = "tool_calls"
        value["choices"].append({"index": 1, "message": {"role": "assistant", "content": "separate"}, "finish_reason": "length"})
        rows, count = self.events(value)
        self.assertEqual(count, 3)
        tools = rows[0]["choices"][0]["delta"]["tool_calls"]
        self.assertEqual([call["index"] for call in tools], [0, 1])
        self.assertEqual(json.loads(tools[0]["function"]["arguments"]),
                         {"path": 'Pilot "Person" 7d84', "secret": "[[secret:nvidia:123456]]"})
        self.assertEqual(json.loads(tools[1]["function"]["arguments"]), {})
        self.assertEqual(rows[0]["choices"][1]["delta"]["content"], "separate")
        self.assertEqual(rows[1]["choices"][1]["finish_reason"], "length")

    def test_json_and_legacy_function_arguments_are_restored_without_stream_bridge(self):
        value = self.completion()
        value["choices"][0]["message"]["function_call"] = {"name": "read", "arguments": '{"name":"identity-1"}'}
        output, count = restore_response(encode(value), self.restore, 1 << 20)
        restored = json.loads(output)
        self.assertEqual(count, 3)
        self.assertEqual(restored["object"], "chat.completion")
        self.assertEqual(json.loads(restored["choices"][0]["message"]["function_call"]["arguments"]), {"name": 'Pilot "Person" 7d84'})

    def test_provider_errors_remain_json_with_secret_placeholders(self):
        value = {"error": {"message": "identity-1 [[secret:nvidia:123456]]"}}
        output, count = restore_response(encode(value), self.restore, 1024)
        self.assertEqual(count, 1)
        self.assertEqual(json.loads(output)["error"]["message"], 'Pilot "Person" 7d84 [[secret:nvidia:123456]]')

    def test_bad_json_or_incomplete_completions_are_refused(self):
        for body in [b"{", b"[]", b"data: [DONE]\n\n", b"NaN", b"{} {}"]:
            with self.subTest(body=body), self.assertRaises((ValueError, TypeError)):
                restore_response(body, self.restore, 1024, stream=True)
        for value in [{}, {"choices": []}, dict(self.completion(), choices=[{"index": 0, "message": {"role": "assistant"}}])]:
            with self.subTest(value=value), self.assertRaises((ValueError, TypeError)):
                self.events(value)
        value = self.completion()
        value["choices"].append(value["choices"][0])
        with self.assertRaises(ValueError):
            self.events(value)

    def test_invalid_tool_arguments_are_refused(self):
        for arguments in ["{", "[]", "null"]:
            value = self.completion()
            value["choices"][0]["message"]["function_call"] = {"name": "read", "arguments": arguments}
            with self.subTest(arguments=arguments), self.assertRaises((ValueError, TypeError)):
                self.events(value)

    def test_input_and_expanded_output_caps_are_enforced(self):
        for size in [5, 22]:
            with self.subTest(size=size), self.assertRaises(ValueError):
                restore_response(b'{"text":"identity-1"}', self.restore, size)
        body = encode(self.completion())
        with self.assertRaises(ValueError):
            restore_response(body, self.restore, len(body) + 100, stream=True)


if __name__ == "__main__":
    unittest.main()
