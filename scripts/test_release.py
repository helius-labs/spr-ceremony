import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('release', Path(__file__).with_name('stage-core-release.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def test_core_rotation_preserves_excluded_entries(self):
        previous = {'prefix': 'proving-keys/old', 'keys': {
            'transfer_confidential_1_2.key': {'sha256': 'a' * 64, 'size': 42},
            'batch_address-append_40_4999.key': {'sha256': 'b' * 64, 'size': 74_000_000_000},
            'merge_ring_8_1.key': {'sha256': 'c' * 64, 'size': 100},
            'batch_address-append_40_10.key': {'sha256': 'd' * 64, 'size': 100},
            'release.key': {'sha256': 'e' * 64, 'size': 100, 'source': 'release'},
        }}
        replacement = {'transfer_confidential_1_2.key': {'sha256': 'f' * 64, 'size': 43}}
        updated = release.update_lock(previous, replacement)
        self.assertNotEqual(updated['prefix'], previous['prefix'])
        for name, ref in previous['keys'].items():
            if name not in replacement:
                self.assertEqual(updated['keys'][name], ref)
        self.assertEqual(previous['keys']['transfer_confidential_1_2.key']['sha256'], 'a' * 64)
        sdk = '\n'.join(f'"{n}": "{e["sha256"]}",' for n, e in previous['keys'].items())
        staged = release.update_sdk(sdk, previous, replacement)
        self.assertIn('f' * 64, staged)
        for char in 'bcde':
            self.assertIn(char * 64, staged)
        with self.assertRaises(ValueError):
            release.update_sdk(sdk.replace('a' * 64, '0' * 64), previous, replacement)


if __name__ == '__main__':
    unittest.main()
