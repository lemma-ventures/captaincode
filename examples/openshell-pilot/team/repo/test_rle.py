import unittest

from rle import decode, encode


class RunLengthTests(unittest.TestCase):
    def test_encode(self):
        self.assertEqual(encode("aaabcc"), "3a1b2c")
        self.assertEqual(encode("a"), "1a")
        self.assertEqual(encode(""), "")
        self.assertEqual(encode("x" * 12), "12x")

    def test_round_trip(self):
        for text in ("aaabcc", "abcd", "zzzzzzzzzzzzq", ""):
            with self.subTest(text=text):
                self.assertEqual(decode(encode(text)), text)

    def test_decode(self):
        self.assertEqual(decode("12x1y"), "x" * 12 + "y")

    def test_invalid(self):
        with self.assertRaises(ValueError):
            encode("a1")
        for data in ("a", "3", "2a3"):
            with self.subTest(data=data), self.assertRaises(ValueError):
                decode(data)
