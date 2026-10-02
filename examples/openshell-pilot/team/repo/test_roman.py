import unittest

from roman import to_roman


class RomanTests(unittest.TestCase):
    def test_additive(self):
        self.assertEqual(to_roman(3), "III")
        self.assertEqual(to_roman(1666), "MDCLXVI")

    def test_subtractive(self):
        self.assertEqual(to_roman(4), "IV")
        self.assertEqual(to_roman(9), "IX")
        self.assertEqual(to_roman(40), "XL")
        self.assertEqual(to_roman(90), "XC")
        self.assertEqual(to_roman(400), "CD")
        self.assertEqual(to_roman(1994), "MCMXCIV")
        self.assertEqual(to_roman(3999), "MMMCMXCIX")

    def test_range(self):
        for number in (0, -1, 4000):
            with self.assertRaises(ValueError):
                to_roman(number)
