import unittest

from hexcolor import parse_hex


class ParseHexTests(unittest.TestCase):
    def test_long_form(self):
        self.assertEqual(parse_hex("#ff8800"), (255, 136, 0))
        self.assertEqual(parse_hex("#0A0B0C"), (10, 11, 12))

    def test_short_form(self):
        self.assertEqual(parse_hex("#FA0"), (255, 170, 0))

    def test_invalid(self):
        for color in ("", "#", "#ff", "#ff88001", "#gg0000", "ff8800", "##ff8800", "#ff 800"):
            with self.subTest(color=color), self.assertRaises(ValueError):
                parse_hex(color)
