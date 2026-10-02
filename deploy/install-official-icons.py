#!/usr/bin/env python3
"""Install verified local brand assets and change only matching icon scalars."""

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import xml.etree.ElementTree as ET

import yaml


def digest(data):
    return hashlib.sha256(data).hexdigest()


def validate_asset(data, suffix):
    if not data or len(data) > 2 * 1024 * 1024:
        raise ValueError('Invalid icon size')
    if suffix == '.svg':
        if b'<!DOCTYPE' in data.upper() or b'<!ENTITY' in data.upper():
            raise ValueError('SVG declarations are not supported')
        root = ET.fromstring(data)
        if root.tag.split('}')[-1] != 'svg':
            raise ValueError('Expected an SVG root')
        for element in root.iter():
            if element.tag.split('}')[-1] in {'script', 'foreignObject'}:
                raise ValueError('Active SVG content is not supported')
            for key, value in element.attrib.items():
                key = key.split('}')[-1].lower()
                if key.startswith('on') or (key in {'href', 'src'} and not value.startswith('#')):
                    raise ValueError('External SVG content is not supported')
            if element.tag.split('}')[-1] == 'style' and re.search(r'@import|url\(\s*["\']?https?:', element.text or '', re.I):
                raise ValueError('External SVG styles are not supported')
    elif suffix == '.png':
        if not data.startswith(b'\x89PNG\r\n\x1a\n'):
            raise ValueError('Expected PNG bytes')
    elif suffix == '.ico':
        if not data.startswith(b'\x00\x00\x01\x00'):
            raise ValueError('Expected ICO bytes')
    else:
        raise ValueError('Unsupported icon format')


def load_assets(directory):
    directory = Path(directory).resolve()
    records = json.loads((directory / 'manifest.json').read_text())['icons']
    result = []
    seen = set()
    for record in records:
        filename = record['file']
        if not re.fullmatch(r'[a-z0-9-]+\.(svg|png|ico)', filename) or filename in seen:
            raise ValueError('Invalid or duplicate asset filename')
        seen.add(filename)
        path = directory / filename
        if path.is_symlink() or not path.is_file():
            raise ValueError('Asset must be a regular file')
        data = path.read_bytes()
        if digest(data) != record['sha256']:
            raise ValueError('Asset checksum mismatch: ' + filename)
        validate_asset(data, path.suffix)
        pattern = re.compile(record['service_name_pattern'], re.I)
        result.append((record, data, pattern))
    return result


def mapping(node):
    if not isinstance(node, yaml.MappingNode) or node.flow_style:
        raise ValueError('Configuration must use block mappings')
    return {key.value: value for key, value in node.value}


def plan(text, assets, service_map=None):
    config = yaml.safe_load(text)
    service_map = service_map or {}
    service_ids = {s['id'] for g in config['groups'] for s in g['services']}
    brands = {a[0]['name']: a for a in assets}
    if set(service_map) - service_ids or set(service_map.values()) - set(brands):
        raise ValueError('Unknown service or brand in private mapping')
    expected = copy.deepcopy(config)
    nodes = mapping(yaml.compose(text))['groups'].value
    prefix = config['assets']['uploads_url_prefix']
    if not re.fullmatch(r'/(?:[A-Za-z0-9_-]+/)+', prefix):
        raise ValueError('Expected a local uploads URL prefix')
    edits, changes, unmatched = [], [], []
    for group, expected_group, group_node in zip(config['groups'], expected['groups'], nodes, strict=True):
        service_nodes = mapping(group_node)['services'].value
        for service, expected_service, service_node in zip(group['services'], expected_group['services'], service_nodes, strict=True):
            matches = ([brands[service_map[service['id']]]] if service['id'] in service_map
                       else [a for a in assets if a[2].fullmatch(service['name'])])
            if len(matches) > 1:
                raise ValueError('Ambiguous brand match')
            if not matches:
                unmatched.append({'id': service['id'], 'name': service['name']})
                continue
            record, _, _ = matches[0]
            source = Path(record['file'])
            filename = source.stem + '-' + record['sha256'][:12] + source.suffix
            icon = prefix + 'icons/official/' + filename
            if service.get('icon', '') == icon:
                continue
            fields = mapping(service_node)
            if 'icon' not in fields:
                raise ValueError('Matching service requires an icon field')
            scalar = fields['icon']
            if not isinstance(scalar, yaml.ScalarNode):
                raise ValueError('Icon must be a scalar')
            edits.append((scalar.start_mark.index, scalar.end_mark.index, json.dumps(icon)))
            expected_service['icon'] = icon
            changes.append({'id': service['id'], 'name': service['name'], 'before': service.get('icon', ''),
                            'after': icon, 'brand': record['name'], 'asset_file': filename})
    candidate = text
    for start, end, replacement in sorted(edits, reverse=True):
        candidate = candidate[:start] + replacement + candidate[end:]
    if yaml.safe_load(candidate) != expected:
        raise ValueError('Changes exceeded the icon fields')
    return candidate, changes, unmatched


def install(config_path, uploads_dir, assets_dir, expected_hash=None, backup_dir=None, apply=False, service_map=None):
    config_path, uploads_dir = Path(config_path).resolve(), Path(uploads_dir).resolve()
    assets = load_assets(assets_dir)
    original_stat = config_path.stat()
    original = config_path.read_bytes()
    if expected_hash and digest(original) != expected_hash:
        raise ValueError('Configuration changed since preflight')
    candidate, changes, unmatched = plan(original.decode(), assets, service_map)
    result = {'applied': False, 'config_before_sha256': digest(original),
              'config_after_sha256': digest(candidate.encode()), 'changes': changes, 'unmatched': unmatched}
    if not apply or not changes:
        return result
    if not expected_hash or not backup_dir:
        raise ValueError('Applying requires a config hash and a new private backup directory')
    if not uploads_dir.is_dir():
        raise ValueError('Uploads mount must already exist')
    destination = uploads_dir / 'icons/official'
    if not destination.resolve().is_relative_to(uploads_dir):
        raise ValueError('Icon directory escapes the uploads mount')
    selected = {c['brand']: c['asset_file'] for c in changes}
    prepared = []
    for record, data, _ in assets:
        if record['name'] not in selected:
            continue
        target = destination / selected[record['name']]
        if target.is_symlink() or (target.exists() and target.read_bytes() != data):
            raise ValueError('Destination asset differs: ' + target.name)
        prepared.append((target, data))
    backup = Path(backup_dir)
    backup.mkdir(mode=0o700)
    (backup / 'services.before.yaml').write_bytes(original)
    os.chmod(backup / 'services.before.yaml', 0o600)
    destination.mkdir(parents=True, exist_ok=True)
    for target, data in prepared:
        if not target.exists():
            with target.open('xb') as file:
                file.write(data)
            os.chmod(target, 0o644)
        if digest(target.read_bytes()) != digest(data):
            raise ValueError('Installed icon verification failed')
    # Preserve the inode of the existing single-file Docker bind mount.
    with config_path.open('r+b') as file:
        if os.fstat(file.fileno()).st_ino != original_stat.st_ino:
            raise ValueError('Configuration file was replaced')
        if file.read() != original:
            raise ValueError('Concurrent configuration change')
        file.seek(0)
        file.write(candidate.encode())
        file.truncate()
        file.flush()
        os.fsync(file.fileno())
    result['applied'] = True
    result['backup_dir'] = str(backup)
    (backup / 'icon-changes.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    os.chmod(backup / 'icon-changes.json', 0o600)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True)
    parser.add_argument('--uploads-dir', required=True)
    parser.add_argument('--assets-dir', default=str(Path(__file__).with_name('official-icons')))
    parser.add_argument('--expect-config-sha256')
    parser.add_argument('--backup-dir')
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--service-map', help='Private JSON object mapping custom service IDs to catalog brand names')
    args = parser.parse_args()
    service_map = json.loads(Path(args.service_map).read_text()) if args.service_map else None
    result = install(args.config, args.uploads_dir, args.assets_dir, args.expect_config_sha256, args.backup_dir, args.apply, service_map)
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
