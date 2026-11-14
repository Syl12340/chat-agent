"""Read-only production smoke check; never sends chat or prints credentials."""

import json
import shlex
import sys
import urllib.error
import urllib.request
from pathlib import Path


BASE = "http://127.0.0.1:9991"
EXPECTED_VERSION = sys.argv[1] if len(sys.argv) > 1 else ""
EXPECTED_CACHE = sys.argv[2] if len(sys.argv) > 2 else ""
EXPECTED_ANDROID = int(sys.argv[3]) if len(sys.argv) > 3 else 0


def access_token() -> str:
    for line in Path("/etc/chat-agent/chat-agent.env").read_text().splitlines():
        if line.startswith("CHAT_ACCESS_TOKEN="):
            values = shlex.split(line.split("=", 1)[1])
            if len(values) == 1 and values[0]:
                return values[0]
    raise RuntimeError("access token is unavailable")


TOKEN = access_token()
CLIENT = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def open_path(path: str, authenticated: bool = True):
    headers = {"Authorization": "Bearer " + TOKEN} if authenticated else {}
    request = urllib.request.Request(BASE + path, headers=headers)
    return CLIENT.open(request, timeout=20)


def json_path(path: str, authenticated: bool = True):
    with open_path(path, authenticated) as response:
        return json.load(response)


assert json_path("/healthz", False)["status"] == "ok"
try:
    json_path("/api/status", False)
    raise AssertionError("unauthenticated status was accepted")
except urllib.error.HTTPError as error:
    assert error.code == 401

status = json_path("/api/status")
if EXPECTED_VERSION:
    assert status["version"] == EXPECTED_VERSION
assert status["scheduler_ok"] is True

with open_path("/sw.js", False) as response:
    service_worker = response.read(64 * 1024).decode("utf-8")
if EXPECTED_CACHE:
    assert EXPECTED_CACHE in service_worker

release = json_path("/api/updates/android")
if EXPECTED_ANDROID:
    assert release["version_code"] == EXPECTED_ANDROID

sessions = json_path("/api/sessions")
summary = {
    "sessions": len(sessions),
    "messages": 0,
    "turns": 0,
    "active": 0,
    "running": 0,
    "queued": 0,
    "paused": 0,
    "unavailable": 0,
}
for item in sessions:
    summary["active"] += int(bool(item.get("active")))
    summary["unavailable"] += int(bool(item.get("unavailable")))
    if item.get("unavailable"):
        continue
    history = json_path("/api/sessions/" + item["id"] + "/messages")
    summary["messages"] += len(history.get("messages", []))
    summary["turns"] += len(history.get("turns", []))
    summary["paused"] += int(bool(history.get("queue_paused")))
    for turn in history.get("turns", []):
        summary["running"] += int(turn.get("status") == "running")
        summary["queued"] += int(turn.get("status") == "queued")

assert summary["active"] == 0
assert summary["running"] == 0
assert summary["queued"] == 0
assert summary["unavailable"] == 0

if not status.get("onebot_enabled"):
    request = urllib.request.Request(
        BASE + "/hooks/onebot/v1/events",
        data=b"{}",
        headers={"Content-Type": "application/json"},
    )
    try:
        CLIENT.open(request, timeout=10)
        raise AssertionError("disabled OneBot webhook was accepted")
    except urllib.error.HTTPError as error:
        assert error.code == 404

print(json.dumps({
    "status": "ok",
    "version": status["version"],
    "model": status["model"],
    "scheduler_ok": status["scheduler_ok"],
    "web_search": status["web_search"],
    "web_fetch": status["web_fetch"],
    "onebot_enabled": status.get("onebot_enabled", False),
    "qq_official_enabled": status.get("qq_official_enabled", False),
    "android_version_code": release["version_code"],
    **summary,
}, ensure_ascii=False, sort_keys=True))
