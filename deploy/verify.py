"""One deployment smoke flow, explicitly limited to demo mode; standard library only."""
import json
from pathlib import Path
import sys
from urllib.error import HTTPError
from urllib.request import Request, urlopen

BASE = "http://127.0.0.1:9991"
token = next(line.split("=", 1)[1].strip() for line in Path(
    "/etc/chat-agent/chat-agent.env").read_text().splitlines()
    if line.startswith("CHAT_ACCESS_TOKEN="))


def request(path, body=None, authenticated=True):
    headers = {"Authorization": "Bearer " + token} if authenticated else {}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = Request(BASE + path, data=None if body is None else json.dumps(body).encode(), headers=headers)
    with urlopen(req, timeout=40) as response:
        return response.read()


assert json.loads(request("/healthz", authenticated=False))["status"] == "ok"
try:
    request("/api/status", authenticated=False)
    raise AssertionError("Unauthenticated API accepted")
except HTTPError as error:
    assert error.code == 401
assert json.loads(request("/api/status"))["demo"] is True, "Demo checks cannot call a live model"
assert b"saveMarkdown" in request("/native.js", authenticated=False)
record = Path("/var/lib/chat-agent/deploy-check-session")
if sys.argv[1] == "before-restart":
    session = json.loads(request("/api/sessions", {}))
    sid = session["id"]
    events = request("/api/sessions/" + sid + "/messages", {"content": "现在几点？请查一下当前 UTC 时间。"})
    assert b"get_current_time" in events and b"UTC" in events
    record.write_text(sid)
    record.chmod(0o600)
    print("PASS: health, API auth, embedded Android export module, demo SSE time tool.")
elif sys.argv[1] == "after-restart":
    messages = json.loads(request("/api/sessions/" + record.read_text().strip() + "/messages"))
    assert "UTC" in json.dumps(messages)
    assert "assistant" in json.dumps(messages)
    print("PASS: service restart restored chat history; health and auth still correct.")
else:
    raise ValueError("Expected before-restart or after-restart")
