import unittest

from ordinal import ordinal


class OrdinalTests(unittest.TestCase):
    def test_units(self):
        self.assertEqual([ordinal(n) for n in (0, 1, 2, 3, 4)], ["0th", "1st", "2nd", "3rd", "4th"])

    def test_teens(self):
        self.assertEqual([ordinal(n) for n in (11, 12, 13, 111, 112)], ["11th", "12th", "13th", "111th", "112th"])

    def test_larger(self):
        self.assertEqual([ordinal(n) for n in (21, 22, 101, 1003)], ["21st", "22nd", "101st", "1003rd"])

    def test_negative(self):
        with self.assertRaises(ValueError):
            ordinal(-1)
