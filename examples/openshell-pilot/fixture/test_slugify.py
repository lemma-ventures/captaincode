import unittest

from slugify import slugify


class SlugifyTests(unittest.TestCase):
    def test_punctuation_and_repeated_separators(self):
        self.assertEqual(slugify("  Hello,   WORLD!  "), "hello-world")

    def test_empty(self):
        self.assertEqual(slugify(" --!? "), "")

    def test_ascii(self):
        self.assertEqual(slugify("Café Déjà Vu 42"), "caf-d-j-vu-42")

    def test_idempotent(self):
        self.assertEqual(slugify("hello-world-42"), "hello-world-42")
