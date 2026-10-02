import unittest

from semver import compare


class CompareTests(unittest.TestCase):
    def test_equal(self):
        self.assertEqual(compare("1.2.3", "1.2.3"), 0)

    def test_numeric_components(self):
        self.assertEqual(compare("1.9.0", "1.10.0"), -1)
        self.assertEqual(compare("2.0.0", "1.99.99"), 1)

    def test_prerelease_is_lower(self):
        self.assertEqual(compare("1.0.0-alpha", "1.0.0"), -1)
        self.assertEqual(compare("1.0.0", "1.0.0-rc.1"), 1)

    def test_prerelease_identifiers(self):
        self.assertEqual(compare("1.0.0-alpha", "1.0.0-beta"), -1)
        self.assertEqual(compare("1.0.0-alpha.2", "1.0.0-alpha.10"), -1)
        self.assertEqual(compare("1.0.0-alpha.1", "1.0.0-alpha.beta"), -1)
        self.assertEqual(compare("1.0.0-alpha", "1.0.0-alpha.1"), -1)

    def test_build_metadata_is_ignored(self):
        self.assertEqual(compare("1.0.0+build.5", "1.0.0"), 0)

    def test_invalid(self):
        for version in ("1.0", "1.0.0.0", "01.0.0", "a.b.c", ""):
            with self.subTest(version=version), self.assertRaises(ValueError):
                compare(version, "1.0.0")
