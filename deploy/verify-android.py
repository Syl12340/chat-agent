"""Root-only loopback verification; never print credentials or call the model."""
import hashlib
import json
import shlex
import urllib.error
import urllib.request
from pathlib import Path

values = {}
for line in Path('/etc/chat-agent/chat-agent.env').read_text().splitlines():
    if line.startswith('CHAT_ACCESS_TOKEN='):
        values['token'] = shlex.split(line.split('=', 1)[1])[0]
base = 'http://127.0.0.1:9991'
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
