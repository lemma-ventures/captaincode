import unittest

from csvline import split_csv_line


class SplitTests(unittest.TestCase):
    def test_plain(self):
        self.assertEqual(split_csv_line("a,b,c"), ["a", "b", "c"])

    def test_quoted_separator(self):
        self.assertEqual(split_csv_line('a,"b,c",d'), ["a", "b,c", "d"])

    def test_doubled_quote(self):
        self.assertEqual(split_csv_line('"say ""hi""",x'), ['say "hi"', "x"])

    def test_empty_fields(self):
        self.assertEqual(split_csv_line("a,,b"), ["a", "", "b"])
        self.assertEqual(split_csv_line(""), [""])

    def test_unterminated_quote(self):
        with self.assertRaises(ValueError):
            split_csv_line('"abc')
