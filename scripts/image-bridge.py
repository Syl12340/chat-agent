#!/usr/bin/env python3
"""Keep the local image bridge running so the server can find it later.

The bridge is a thin HTTP shim in front of ComfyUI. It is useful to have it up
even when ComfyUI is closed: the server asks /health, learns ComfyUI is offline,
and can say so. Without the bridge the answer is "the bridge is not reachable",
which is a different and less useful fact.

ComfyUI itself is a GUI application and stays a person's decision — this only
guarantees the part that can be automatic.

Usage:
    python scripts/image-bridge.py            # start if needed, then watch
    python scripts/image-bridge.py --check    # report state and exit
    python scripts/image-bridge.py --stop     # stop a bridge started this way
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
BINARY = ROOT / "bin" / "imagebridge.exe"
PYTHON = Path(r"E:\IAST\image-generation\.venv\Scripts\python.exe")
SCRIPT = Path(r"E:\IAST\image-generation\agent-bridge\imgctl.py")
DEFAULT_PROFILE = "z_anime_d8"
COMFY_URL = "http://127.0.0.1:8188"
LISTEN = "127.0.0.1:18190"


def http_json(url: str, timeout: float = 5.0):
    try:
        with urllib.request.urlopen(url, timeout=timeout) as response:
            return response.status, json.loads(response.read() or b"null")
    except urllib.error.HTTPError as exc:
        # /health answers 503 with a full body when ComfyUI is closed; dropping it
        # would throw away the diagnosis along with the status.
        try:
            return exc.code, json.loads(exc.read() or b"null")
        except Exception:
            return exc.code, None
    except Exception:
        return None, None


def bridge_up() -> bool:
    """True when the bridge answers at all.

    /health returns 200 when ComfyUI is reachable and 503 when it is not. Both mean
    the bridge is running — 503 carries the useful fact that ComfyUI is closed. Only
    "no answer" means the bridge is missing. The probe can take a few seconds while
    the bridge tries ComfyUI, so the timeout has to outlast that attempt.
    """
    status, _ = http_json(f"http://{LISTEN}/health", timeout=15)
    return status in (200, 503)


def comfy_up() -> bool:
    status, _ = http_json(f"{COMFY_URL}/system_stats", timeout=2)
    return status == 200


def report() -> None:
    print(f"  bridge   127.0.0.1:18190  {'up' if bridge_up() else 'down'}")
    print(f"  comfyui  127.0.0.1:8188   {'up' if comfy_up() else 'down'}")
    if bridge_up():
        _, body = http_json(f"http://{LISTEN}/health", timeout=15)
        if isinstance(body, dict):
            print(f"  comfy_online={body.get('comfy')} bridge={body.get('bridge', '?')}")


def start() -> subprocess.Popen:
    if not BINARY.exists():
        raise SystemExit(f"imagebridge not found at {BINARY}; build it with: go build -o bin/imagebridge.exe ./cmd/imagebridge")
    if not PYTHON.exists():
        raise SystemExit(f"the ComfyUI interpreter is missing: {PYTHON}")
    if not SCRIPT.exists():
        raise SystemExit(f"imgctl.py is missing: {SCRIPT}")

    env = dict(os.environ)
    # The binary reads these itself, so no command line is needed and the paths
    # stay out of the process list.
    env["IMAGEBRIDGE_LISTEN"] = LISTEN
    env["IMAGEBRIDGE_PYTHON"] = str(PYTHON)
    env["IMAGEBRIDGE_SCRIPT"] = str(SCRIPT)
    env["IMAGEBRIDGE_PROFILE"] = DEFAULT_PROFILE

    creation = subprocess.CREATE_NO_WINDOW if sys.platform == "win32" else 0
    process = subprocess.Popen(
        [str(BINARY)],
        cwd=str(ROOT),
        env=env,
        creationflags=creation,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    for _ in range(20):
        time.sleep(0.4)
        if bridge_up():
            return process
        if process.poll() is not None:
            raise SystemExit(f"imagebridge exited immediately (code {process.returncode})")
    raise SystemExit("imagebridge started but /health never answered")


def main() -> int:
    parser = argparse.ArgumentParser(description="Keep the local image bridge running.")
    parser.add_argument("--check", action="store_true", help="report state and exit")
    parser.add_argument("--stop", action="store_true", help="stop the bridge if it is running")
    parser.add_argument("--once", action="store_true", help="start if needed and exit without watching")
    options = parser.parse_args()

    if options.check:
        report()
        return 0

    if options.stop:
        subprocess.run(["taskkill", "/IM", "imagebridge.exe", "/F"], capture_output=True)
        print("stopped" if not bridge_up() else "still running")
        return 0

    if bridge_up():
        print("bridge already running")
        report()
        return 0

    print(f"starting imagebridge (ComfyUI {'up' if comfy_up() else 'down'})")
    process = start()
    report()
    if options.once:
        return 0

    print("watching; press Ctrl+C to stop the bridge")
    try:
        while process.poll() is None:
            time.sleep(2)
        print(f"bridge exited (code {process.returncode})")
    except KeyboardInterrupt:
        print("\nstopping bridge")
    finally:
        if process.poll() is None:
            process.terminate()
    return 0


if __name__ == "__main__":
    sys.exit(main())
