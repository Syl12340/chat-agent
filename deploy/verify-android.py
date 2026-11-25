"""Root-only loopback verification; never print credentials or call the model."""
import hashlib
import json
import shlex
import sys
import urllib.error
import urllib.request
from pathlib import Path

config = Path(sys.argv[1] if len(sys.argv) > 1 else '/etc/chat-agent/chat-agent.env')
base = sys.argv[2] if len(sys.argv) > 2 else 'http://127.0.0.1:9991'
values = {}
for line in config.read_text().splitlines():
    if line.startswith('CHAT_ACCESS_TOKEN='):
        values['token'] = shlex.split(line.split('=', 1)[1])[0]
client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
for path in ('/api/updates/android', '/api/updates/android/unauthenticated/app.apk'):
    try:
        client.open(base + path, timeout=5)
        raise RuntimeError('Unauthenticated update accepted')
    except urllib.error.HTTPError as error:
        assert error.code == 401

def get(path):
    request = urllib.request.Request(base + path, headers={'Authorization': 'Bearer ' + values['token']})
    return client.open(request, timeout=30)

with get('/api/updates/android') as response:
    release = json.load(response)
digest = hashlib.sha256()
size = 0
with get(release['apk_path']) as response:
    while data := response.read(65536):
        size += len(data)
        assert size <= 64 * 1024 * 1024
        digest.update(data)
assert size == release['size'] and digest.hexdigest() == release['sha256']
print(json.dumps({'verified': True, 'auth_required': True, **release}))
