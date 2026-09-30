import unittest

from wrap import wrap


class WrapTests(unittest.TestCase):
    def test_greedy(self):
        self.assertEqual(wrap("the quick brown fox", 10), ["the quick", "brown fox"])

    def test_lines_never_exceed_width(self):
        self.assertEqual(wrap("abc de", 5), ["abc", "de"])

    def test_whitespace_runs(self):
        self.assertEqual(wrap("  a   b  ", 10), ["a b"])

    def test_long_word_gets_its_own_line(self):
        self.assertEqual(wrap("abcdefghijk xy", 5), ["abcdefghijk", "xy"])

    def test_empty(self):
        self.assertEqual(wrap("", 5), [])
        self.assertEqual(wrap("   ", 5), [])

    def test_width(self):
        with self.assertRaises(ValueError):
            wrap("a", 0)
