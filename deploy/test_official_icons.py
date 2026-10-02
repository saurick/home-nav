import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

import yaml

spec = importlib.util.spec_from_file_location('official_icons', Path(__file__).with_name('install-official-icons.py'))
icons = importlib.util.module_from_spec(spec)
spec.loader.exec_module(icons)


class OfficialIconDeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.assets = self.root / 'assets'
        self.assets.mkdir()
        self.uploads = self.root / 'uploads'
        self.uploads.mkdir()
        self.config = self.root / 'services.yaml'
        self.original = '''# Keep operator comments and private fields.
auth:
  username: fixture-user
  password: fixture-password
  session_secret: fixture-session
assets:
  uploads_url_prefix: /uploads/
groups:
  - id: fixture-group
    name: Services
    services:
      - id: fixture-rustdesk
        name: RustDesk
        icon: '' # Existing comment
        external_url: https://example.invalid/admin/
        health: {type: disabled}
      - id: custom
        name: Custom
        icon: mdi:web
'''
        self.config.write_text(self.original)
        self.config.chmod(0o600)
        self.data = b'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0h24v24H0z"/></svg>'
        self.record = {'name': 'rustdesk', 'file': 'rustdesk.svg',
                       'sha256': icons.digest(self.data), 'service_name_pattern': 'rustdesk'}
        self.write_asset()

    def write_asset(self):
        (self.assets / 'rustdesk.svg').write_bytes(self.data)
        (self.assets / 'manifest.json').write_text(json.dumps({'icons': [self.record]}))

    def apply(self):
        return icons.install(self.config, self.uploads, self.assets,
            icons.digest(self.original.encode()), self.root / 'backup', True)

    def test_apply_preserves_auth_order_urls_comments_mode_inode_and_supports_noop(self):
        inode = self.config.stat().st_ino
        report = self.apply()
        before, after = yaml.safe_load(self.original), yaml.safe_load(self.config.read_text())
        before['groups'][0]['services'][0]['icon'] = report['changes'][0]['after']
        self.assertEqual(before, after)
        self.assertIn("# Existing comment", self.config.read_text())
        self.assertEqual(inode, self.config.stat().st_ino)
        self.assertEqual(0o600, self.config.stat().st_mode & 0o777)
        self.assertEqual(self.original, (self.root / 'backup/services.before.yaml').read_text())
        self.assertEqual(0o700, (self.root / 'backup').stat().st_mode & 0o777)
        self.assertEqual(self.data, (self.uploads / report['changes'][0]['after'].removeprefix('/uploads/')).read_bytes())
        again = icons.install(self.config, self.uploads, self.assets, apply=False)
        self.assertEqual([], again['changes'])

    def test_dry_run_has_no_writes(self):
        result = icons.install(self.config, self.uploads, self.assets)
        self.assertEqual(1, len(result['changes']))
        self.assertEqual(self.original, self.config.read_text())
        self.assertEqual([], list(self.uploads.iterdir()))

    def test_stale_config_and_tampered_assets_fail_before_mutation(self):
        with self.assertRaisesRegex(ValueError, 'Configuration changed'):
            icons.install(self.config, self.uploads, self.assets, '0' * 64, self.root / 'backup', True)
        (self.assets / 'rustdesk.svg').write_bytes(b'tampered')
        with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
            self.apply()
        self.assertEqual(self.original, self.config.read_text())
        self.assertFalse((self.root / 'backup').exists())

    def test_upload_symlink_and_existing_asset_conflict_are_preserved(self):
        outside = self.root / 'outside'
        outside.mkdir()
        (self.uploads / 'icons').symlink_to(outside, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, 'escapes'):
            self.apply()
        (self.uploads / 'icons').unlink()
        destination = self.uploads / 'icons/official'
        destination.mkdir(parents=True)
        existing = destination / ('rustdesk-' + self.record['sha256'][:12] + '.svg')
        existing.write_bytes(b'unrelated')
        with self.assertRaisesRegex(ValueError, 'differs'):
            self.apply()
        self.assertEqual(b'unrelated', existing.read_bytes())
        self.assertEqual(self.original, self.config.read_text())

    def test_svg_rejects_active_and_external_content(self):
        for body in [b'<svg><script>alert(1)</script></svg>', b'<svg onload="alert(1)"/>',
                     b'<svg><image href="https://example.invalid/tracker.png"/></svg>']:
            with self.assertRaises(ValueError):
                icons.validate_asset(body, '.svg')

    def test_private_mapping_handles_custom_names_and_rejects_unknown_assignments(self):
        assets = icons.load_assets(self.assets)
        _, changes, _ = icons.plan(self.original, assets, {'custom': 'rustdesk'})
        self.assertEqual({'fixture-rustdesk', 'custom'}, {c['id'] for c in changes})
        for assignment in [{'missing': 'rustdesk'}, {'custom': 'missing'}]:
            with self.assertRaisesRegex(ValueError, 'Unknown service or brand'):
                icons.plan(self.original, assets, assignment)


if __name__ == '__main__':
    unittest.main()
