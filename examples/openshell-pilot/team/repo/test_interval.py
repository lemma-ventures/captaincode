import unittest

from interval import merge


class MergeTests(unittest.TestCase):
    def test_overlapping(self):
        self.assertEqual(merge([(1, 3), (2, 6), (8, 10)]), [(1, 6), (8, 10)])

    def test_unsorted_input(self):
        self.assertEqual(merge([(8, 10), (1, 3), (2, 6)]), [(1, 6), (8, 10)])

    def test_touching(self):
        self.assertEqual(merge([(1, 2), (2, 3)]), [(1, 3)])

    def test_contained(self):
        self.assertEqual(merge([(1, 10), (2, 3)]), [(1, 10)])

    def test_empty(self):
        self.assertEqual(merge([]), [])

    def test_input_is_not_modified(self):
        intervals = [(5, 6), (1, 2)]
        merge(intervals)
        self.assertEqual(intervals, [(5, 6), (1, 2)])

    def test_reversed_interval(self):
        with self.assertRaises(ValueError):
            merge([(3, 1)])
