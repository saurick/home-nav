import importlib.util
import io
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("lan_cert_sync", Path(__file__).with_name("lan-cert-sync.py"))
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class CertificateSyncTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workspace = tempfile.TemporaryDirectory()
        cls.root = Path(cls.workspace.name)
        cls.ca = cls.root / "ca.pem"
        ca_key = cls.root / "ca.key"
        sync.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
                  "-nodes", "-keyout", str(ca_key), "-out", str(cls.ca), "-days", "365",
                  "-subj", "/CN=Certificate Sync Test CA", "-addext", "basicConstraints=critical,CA:TRUE"])
        extension = cls.root / "extensions.cnf"
        extension.write_text("subjectAltName=DNS:*.example.test\nextendedKeyUsage=serverAuth\n")
        cls.bundles = {}
        for name, days in [("old", 20), ("new", 30), ("short", 1)]:
            key, request, leaf = [cls.root / (name + suffix) for suffix in [".key", ".csr", ".leaf"]]
            sync.run(["openssl", "req", "-new", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
                      "-nodes", "-keyout", str(key), "-out", str(request), "-subj", "/CN=*.example.test"])
            sync.run(["openssl", "x509", "-req", "-in", str(request), "-CA", str(cls.ca),
                      "-CAkey", str(ca_key), "-set_serial", str(days), "-out", str(leaf),
                      "-days", str(days), "-extfile", str(extension)])
            cert = cls.root / (name + ".pem")
            cert.write_bytes(leaf.read_bytes() + cls.ca.read_bytes())
            cls.bundles[name] = (cert, key)

    @classmethod
    def tearDownClass(cls):
        cls.workspace.cleanup()

    def test_certificate_validation(self):
        cert, key = self.bundles["new"]
        fingerprint = sync.validate_bundle(cert, key, ["app-lan.example.test"], 14, str(self.ca))
        self.assertEqual(len(fingerprint), 64)
        for invalid_cert, invalid_key, hosts, ca in [
            (cert, self.bundles["old"][1], ["app-lan.example.test"], str(self.ca)),
            (*self.bundles["short"], ["app-lan.example.test"], str(self.ca)),
            (cert, key, ["outside.example.org"], str(self.ca)),
            (cert, key, ["app-lan.example.test"], None),
        ]:
            with self.subTest(hosts=hosts, ca=ca), self.assertRaises(sync.SyncError):
                sync.validate_bundle(invalid_cert, invalid_key, hosts, 14, ca)

    def test_bundle_rejects_extra_paths_and_symlinks(self):
        for name, kind in [("../privkey.pem", tarfile.REGTYPE), ("privkey.pem", tarfile.SYMTYPE)]:
            data = io.BytesIO()
            with tarfile.open(fileobj=data, mode="w") as archive:
                for member_name in ["fullchain.pem", name]:
                    member = tarfile.TarInfo(member_name)
                    member.type = kind if member_name == name else tarfile.REGTYPE
                    archive.addfile(member)
            with tempfile.TemporaryDirectory() as directory, self.assertRaises(sync.SyncError):
                sync.unpack_bundle(data.getvalue(), Path(directory))

    def config(self, directory, old="old"):
        root = Path(directory)
        cert, key = self.bundles[old]
        deployed_cert, deployed_key = root / "cert.pem", root / "key.pem"
        deployed_cert.write_bytes(cert.read_bytes())
        deployed_key.write_bytes(key.read_bytes())
        deployed_cert.chmod(0o640)
        deployed_key.chmod(0o600)
        return {
            "state_dir": directory, "ca_file": str(self.ca),
            "targets": [{"name": "test-gateway", "cert_file": str(deployed_cert),
                         "key_file": str(deployed_key), "uid": os.getuid(), "gid": os.getgid(),
                         "check_command": ["check-test"], "reload_command": ["reload-test"]}],
            "probes": [{"hostname": "app-lan.example.test", "address": "127.0.0.1"}],
        }

    def test_dry_run_and_older_certificate_never_change_files(self):
        with tempfile.TemporaryDirectory() as directory:
            config = self.config(directory)
            report = sync.deploy_bundle(config, *self.bundles["new"], dry_run=True)
            self.assertEqual(report["changed_targets"], ["test-gateway"])
            self.assertEqual(Path(config["targets"][0]["cert_file"]).read_bytes(), self.bundles["old"][0].read_bytes())
            config = self.config(directory, "new")
            with self.assertRaises(sync.SyncError):
                sync.deploy_bundle(config, *self.bundles["old"])

    def test_reload_failure_restores_both_files_and_permissions(self):
        actual_run = sync.run
        reloads = []

        def fake_run(command, **kwargs):
            if command == ("check-test",):
                return b""
            if command == ("reload-test",):
                reloads.append(command)
                if len(reloads) == 1:
                    raise sync.SyncError("test reload failure")
                return b""
            return actual_run(command, **kwargs)

        with tempfile.TemporaryDirectory() as directory:
            config = self.config(directory)
            with patch.object(sync, "run", side_effect=fake_run), self.assertRaisesRegex(sync.SyncError, "previous files restored"):
                sync.deploy_bundle(config, *self.bundles["new"])
            for field, source, mode in [("cert_file", self.bundles["old"][0], 0o640), ("key_file", self.bundles["old"][1], 0o600)]:
                path = Path(config["targets"][0][field])
                self.assertEqual(path.read_bytes(), source.read_bytes())
                self.assertEqual(path.stat().st_mode & 0o777, mode)
            self.assertEqual(len(reloads), 2)

    def test_unchanged_files_still_require_live_tls_verification(self):
        with tempfile.TemporaryDirectory() as directory:
            config = self.config(directory, "new")
            with patch.object(sync, "verify_probes", side_effect=sync.SyncError("test TLS mismatch")) as probe:
                with self.assertRaisesRegex(sync.SyncError, "TLS mismatch"):
                    sync.deploy_bundle(config, *self.bundles["new"])
                probe.assert_called_once()
            self.assertFalse(list(Path(directory).glob("rollback-*")))


if __name__ == "__main__":
    unittest.main()
