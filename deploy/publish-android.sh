#!/bin/sh
# Stage app.apk + manifest.json next to this script, then run as root.
set -eu
umask 077
[ "$(id -u)" = 0 ] || { echo 'Run as root.' >&2; exit 1; }
release=${1:?release ID required}
case "$release" in ''|*[!a-zA-Z0-9_-]*) echo 'Invalid release ID.' >&2; exit 1 ;; esac
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
python3 - "$here" "$release" <<'PY'
import grp, hashlib, json, os, pathlib, shutil, sys
source, release = pathlib.Path(sys.argv[1]), sys.argv[2]
apk = source / 'app.apk'
metadata = json.loads((source / 'manifest.json').read_text())
assert set(metadata) == {'version_code', 'version_name', 'package_name', 'size', 'sha256'}
assert type(metadata['version_code']) is int and 1 <= metadata['version_code'] <= 2100000000
assert isinstance(metadata['version_name'], str) and 1 <= len(metadata['version_name']) <= 64
assert metadata['package_name'] in ('dev.chatagent.ai.debug', 'dev.chatagent.ai')
assert 0 < apk.stat().st_size <= 64 * 1024 * 1024
assert metadata['size'] == apk.stat().st_size
assert metadata['sha256'] == hashlib.file_digest(apk.open('rb'), 'sha256').hexdigest()
root = pathlib.Path('/opt/chat-agent/android')
current = root / 'current'
if current.exists():
    old = json.loads((current / 'manifest.json').read_text())
    # Two rules, and the package check comes first on purpose. Within one package
    # the version must only move forward, because that is what makes an upgrade an
    # upgrade. Across packages there is no ordering to preserve: a new application
    # id starts its own numbering, so the baseline legitimately resets.
    if old['package_name'] == metadata['package_name']:
        assert old['version_code'] < metadata['version_code'], 'Version must increase'
    else:
        print('package changed: %s -> %s (version baseline resets)' % (old['package_name'], metadata['package_name']))
target = root / 'releases' / release
assert not target.exists(), 'Release already exists'
target.mkdir(parents=True)
gid = grp.getgrnam('chat-agent').gr_gid
for path in (root, root / 'releases', target):
    os.chown(path, 0, gid); path.chmod(0o750)
for name in ('app.apk', 'manifest.json'):
    dest = target / name
    shutil.copyfile(source / name, dest); os.chown(dest, 0, gid); dest.chmod(0o640)
link = root / ('current.' + release)
link.symlink_to(target); os.replace(link, current)
print(json.dumps({'published': release, **metadata}, ensure_ascii=False))
PY
