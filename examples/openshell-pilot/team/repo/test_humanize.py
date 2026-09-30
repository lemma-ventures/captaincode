import unittest

from humanize import human_bytes


class HumanBytesTests(unittest.TestCase):
    def test_bytes(self):
        self.assertEqual(human_bytes(0), "0 B")
        self.assertEqual(human_bytes(1023), "1023 B")

    def test_one_decimal_above_bytes(self):
        self.assertEqual(human_bytes(1024), "1.0 KiB")
        self.assertEqual(human_bytes(1100), "1.1 KiB")
        self.assertEqual(human_bytes(5 * 1024**3), "5.0 GiB")

    def test_rounding_moves_to_the_next_unit(self):
        self.assertEqual(human_bytes(1024**2 - 1), "1.0 MiB")

    def test_large(self):
        self.assertEqual(human_bytes(3 * 1024**4), "3.0 TiB")
        self.assertEqual(human_bytes(2048 * 1024**5), "2048.0 PiB")

    def test_negative(self):
        with self.assertRaises(ValueError):
            human_bytes(-1)
