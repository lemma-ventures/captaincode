import unittest

from duration import parse_duration


class ParseTests(unittest.TestCase):
    def test_single_unit(self):
        self.assertEqual(parse_duration("90s"), 90)
        self.assertEqual(parse_duration("2h"), 7200)

    def test_combined(self):
        self.assertEqual(parse_duration("1h30m15s"), 5415)
        self.assertEqual(parse_duration("1h15s"), 3615)

    def test_invalid(self):
        for text in ("", "5", "5x", "h", "1s1h", "1h1h", "-5s", " 5s"):
            with self.subTest(text=text), self.assertRaises(ValueError):
                parse_duration(text)
