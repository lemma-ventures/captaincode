import unittest

from camel import to_snake


class SnakeTests(unittest.TestCase):
    def test_camel(self):
        self.assertEqual(to_snake("parseResponse"), "parse_response")

    def test_pascal(self):
        self.assertEqual(to_snake("ParseResponse"), "parse_response")

    def test_acronyms(self):
        self.assertEqual(to_snake("parseHTTPResponse"), "parse_http_response")
        self.assertEqual(to_snake("HTTPServer"), "http_server")
        self.assertEqual(to_snake("userID"), "user_id")

    def test_digits(self):
        self.assertEqual(to_snake("version2Update"), "version2_update")

    def test_unchanged(self):
        self.assertEqual(to_snake("already_snake"), "already_snake")
        self.assertEqual(to_snake(""), "")
