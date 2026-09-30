#!/usr/bin/env python3
"""Synchronize an existing wildcard certificate to configured LAN gateways."""

import argparse
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tarfile
import tempfile
import time


class SyncError(Exception):
    pass


def run(command, *, data=None):
    try:
        result = subprocess.run(command, input=data, capture_output=True, timeout=60)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise SyncError(f"{command[0]} did not complete") from error
    if result.returncode:
        # Command diagnostics may contain private paths; report the failed step only.
        raise SyncError(f"{command[0]} exited with {result.returncode}")
    return result.stdout


def unpack_bundle(data, directory):
    if len(data) > 2 * 1024 * 1024:
        raise SyncError("certificate bundle is too large")
    expected = {"fullchain.pem", "privkey.pem"}
    try:
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:") as archive:
            members = archive.getmembers()
            if len(members) != 2 or {m.name for m in members} != expected:
                raise SyncError("certificate bundle must contain exactly the two PEM files")
            for member in members:
                if not member.isfile() or member.size > 1024 * 1024:
                    raise SyncError("certificate bundle contains an invalid member")
                path = directory / member.name
                path.write_bytes(archive.extractfile(member).read())
                path.chmod(0o600)
    except (tarfile.TarError, OSError) as error:
        raise SyncError("certificate bundle cannot be read") from error
    return directory / "fullchain.pem", directory / "privkey.pem"


def fetch_bundle(source, directory):
    data = run([
        "ssh", "-i", source["identity_file"], "-o", "IdentitiesOnly=yes",
        "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
        "-o", "UserKnownHostsFile=" + source["known_hosts_file"],
        "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=10",
        "-o", "ServerAliveCountMax=2", source["ssh_target"], "cert-bundle",
    ])
    return unpack_bundle(data, directory)


def certificate_der(path):
    return run(["openssl", "x509", "-in", str(path), "-outform", "DER"])


def expires_at(path):
    value = run(["openssl", "x509", "-in", str(path), "-noout", "-enddate"])
    return ssl.cert_time_to_seconds(value.decode().strip().split("=", 1)[1])


def validate_bundle(cert, key, hostnames, minimum_days, ca_file=None):
    run(["openssl", "x509", "-in", str(cert), "-noout", "-checkend", str(minimum_days * 86400)])
    verify = ["openssl", "verify", "-purpose", "sslserver", "-untrusted", str(cert)]
    if ca_file:
        verify += ["-CAfile", ca_file]
    run(verify + [str(cert)])
    for hostname in hostnames:
        run(["openssl", "x509", "-in", str(cert), "-noout", "-checkhost", hostname])
    public = run(["openssl", "x509", "-in", str(cert), "-pubkey", "-noout"])
    cert_public = run(["openssl", "pkey", "-pubin", "-outform", "DER"], data=public)
    key_public = run(["openssl", "pkey", "-in", str(key), "-pubout", "-outform", "DER"])
    if cert_public != key_public:
        raise SyncError("certificate and private key do not match")
    return hashlib.sha256(certificate_der(cert)).hexdigest()


def replace_file(path, data, uid, gid, mode):
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".cert-sync-", delete=False) as file:
            temporary = Path(file.name)
            file.write(data)
            file.flush()
            os.fsync(file.fileno())
            if uid != os.getuid() or gid != os.getgid():
                os.fchown(file.fileno(), uid, gid)
            os.fchmod(file.fileno(), mode)
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if temporary and temporary.exists():
            temporary.unlink()


def commands(targets, name):
    # Two certificate directories may belong to the same Nginx process.
    return list(dict.fromkeys(tuple(target[name]) for target in targets))


def verify_probes(probes, fingerprint, ca_file=None):
    context = ssl.create_default_context(cafile=ca_file)
    for probe in probes:
        last_error = None
        for attempt in range(5):
            try:
                with socket.create_connection((probe["address"], probe.get("port", 443)), timeout=5) as connection:
                    with context.wrap_socket(connection, server_hostname=probe["hostname"]) as tls:
                        if hashlib.sha256(tls.getpeercert(binary_form=True)).hexdigest() != fingerprint:
                            raise SyncError("gateway is serving a different certificate")
                break
            except (OSError, SyncError) as error:
                last_error = error
                if attempt < 4:
                    time.sleep(1)
        else:
            raise SyncError(f"TLS check failed for {probe['hostname']}") from last_error


def deploy_bundle(config, cert, key, *, dry_run=False):
    probes = config["probes"]
    if not probes or not config["targets"]:
        raise SyncError("at least one target and TLS probe are required")
    fingerprint = validate_bundle(cert, key, {p["hostname"] for p in probes},
                                  config.get("minimum_valid_days", 14), config.get("ca_file"))
    incoming = {"cert_file": cert.read_bytes(), "key_file": key.read_bytes()}
    changed = []
    for target in config["targets"]:
        for name in incoming:
            path = Path(target[name])
            if path.is_symlink() or not path.is_file():
                raise SyncError("certificate targets must be existing regular files")
        if expires_at(Path(target["cert_file"])) > expires_at(cert):
            raise SyncError("refusing a certificate with an earlier expiry")
        if any(Path(target[name]).read_bytes() != data for name, data in incoming.items()):
            changed.append(target)
    report = {"fingerprint": fingerprint, "expires_at": expires_at(cert),
              "changed_targets": [t["name"] for t in changed], "dry_run": dry_run}
    if dry_run:
        return report

    snapshots = []
    if changed:
        backup = Path(config["state_dir"]) / (time.strftime("rollback-%Y%m%dT%H%M%SZ", time.gmtime()) + "-" + fingerprint[:12])
        backup.mkdir(mode=0o700)
        for index, target in enumerate(changed):
            for name in incoming:
                path = Path(target[name])
                stat = path.stat()
                saved = backup / f"{index}-{name}.pem"
                saved.write_bytes(path.read_bytes())
                saved.chmod(0o600)
                snapshots.append((path, saved, stat.st_uid, stat.st_gid, stat.st_mode & 0o777))
        manifest = backup / "manifest.json"
        manifest.write_text(json.dumps([
            {"target": str(path), "backup": str(saved), "uid": uid, "gid": gid, "mode": oct(mode)}
            for path, saved, uid, gid, mode in snapshots
        ], indent=2) + "\n")
        manifest.chmod(0o600)
        report["rollback_dir"] = str(backup)

    try:
        for target in changed:
            for name, data in incoming.items():
                replace_file(Path(target[name]), data, target.get("uid", 0), target.get("gid", 0),
                             int(target.get("mode", "0600"), 8))
        for command in commands(changed, "check_command"):
            run(command)
        for command in commands(changed, "reload_command"):
            run(command)
        verify_probes(probes, fingerprint, config.get("ca_file"))
    except Exception as error:
        if snapshots:
            try:
                for path, saved, uid, gid, mode in snapshots:
                    replace_file(path, saved.read_bytes(), uid, gid, mode)
                for command in commands(changed, "check_command"):
                    run(command)
                for command in commands(changed, "reload_command"):
                    run(command)
            except Exception as rollback_error:
                raise SyncError(f"sync and rollback failed; restore files from {backup}") from rollback_error
            raise SyncError(f"sync failed; previous files restored from {backup}") from error
        raise
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True)
    parser.add_argument("--dry-run", action="store_true")
    arguments = parser.parse_args()
    os.umask(0o077)
    config = json.loads(Path(arguments.config).read_text())
    state = Path(config["state_dir"])
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    with (state / "sync.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            with tempfile.TemporaryDirectory(prefix=".fetch-", dir=state) as workspace:
                cert, key = fetch_bundle(config["source"], Path(workspace))
                report = deploy_bundle(config, cert, key, dry_run=arguments.dry_run)
            report.update(result="ok", checked_at=time.time())
        except Exception as error:
            report = {"result": "failed", "checked_at": time.time(), "error": str(error)}
        (state / "last-result.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report))
        return 0 if report["result"] == "ok" else 1


if __name__ == "__main__":
    raise SystemExit(main())
