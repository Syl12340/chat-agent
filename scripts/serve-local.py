#!/usr/bin/env python3
"""Open a local port that reaches the deployed chat-agent from this machine.

The server listens on 127.0.0.1:9991 only, so a browser here has no route to it.
This forwards a local port over SSH to that loopback address and keeps it open
until you press Ctrl+C.

It will not print an address that does not work: the server's /healthz is checked
before the forward starts, and the local port is probed through the tunnel before
the URL is shown.

Examples:
    python scripts/serve-local.py
    python scripts/serve-local.py --port 8080 --show-token
    python scripts/serve-local.py --with-image-bridge
"""

from __future__ import annotations

import argparse
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

DEFAULT_TARGET = "root@47.117.40.74"
REMOTE_PORT = 9991  # chat-agent's loopback port on the server
IMAGE_BRIDGE_PORT = 18190


def port_free(port: int) -> bool:
    """True when nothing is listening on the loopback port."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            probe.bind(("127.0.0.1", port))
        except OSError:
            return False
    return True


def pick_port(preferred: int, span: int = 40) -> int:
    for candidate in range(preferred, preferred + span):
        if port_free(candidate):
            return candidate
    raise SystemExit(f"no free local port in {preferred}..{preferred + span - 1}")


def http_status(url: str, timeout: float = 5.0) -> int | None:
    try:
        with urllib.request.urlopen(url, timeout=timeout) as response:
            return response.status
    except urllib.error.HTTPError as exc:
        return exc.code
    except Exception:
        return None


def ssh(*args: str, timeout: float = 20.0) -> str:
    """Run a short remote command and return its stdout."""
    result = subprocess.run(
        ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", *args],
        capture_output=True,
        timeout=timeout,
    )
    if result.returncode != 0:
        message = result.stderr.decode("utf-8", "replace").strip()
        raise SystemExit(f"ssh failed ({result.returncode}): {message or 'no output'}")
    return result.stdout.decode("utf-8", "replace").strip()


def read_token(target: str) -> str:
    raw = ssh(target, "grep -m1 '^CHAT_ACCESS_TOKEN=' /etc/chat-agent/chat-agent.env")
    return raw.split("=", 1)[1] if "=" in raw else ""


def image_bridge_running() -> bool:
    """True when an imagebridge process already exists on this machine."""
    if sys.platform == "win32":
        result = subprocess.run(
            ["tasklist", "/FI", "IMAGENAME eq imagebridge.exe"],
            capture_output=True,
        )
        return b"imagebridge.exe" in result.stdout.lower()
    result = subprocess.run(["pgrep", "-x", "imagebridge"], capture_output=True)
    return result.returncode == 0


def require_image_bridge() -> None:
    """Refuse to forward the reverse port unless the bridge is actually running.

    The bridge needs its interpreter, script and profile paths, which this script
    does not own; starting it bare would produce a process that answers but has
    nothing behind it. Point at the dedicated launcher instead.
    """
    if image_bridge_running():
        print("    imagebridge is up", flush=True)
        return
    raise SystemExit(
        "imagebridge is not running. Start it first (scripts/tunnel.ps1 starts it "
        "with the ComfyUI paths), then re-run with --with-image-bridge."
    )


def main() -> int:
    parser = argparse.ArgumentParser(description="Forward a local port to the deployed chat-agent.")
    parser.add_argument("--port", type=int, default=9992, help="preferred local port (default 9992)")
    parser.add_argument("--target", default=DEFAULT_TARGET, help=f"ssh destination (default {DEFAULT_TARGET})")
    parser.add_argument("--show-token", action="store_true", help="print the access token from the server")
    parser.add_argument(
        "--with-image-bridge",
        action="store_true",
        help="also forward the reverse image-bridge port so the server can reach ComfyUI here",
    )
    options = parser.parse_args()

    if not shutil.which("ssh"):
        raise SystemExit("ssh is not on PATH; install the OpenSSH client first")

    print("==> checking the server is up", flush=True)
    remote = ssh(options.target, f"curl -s -o /dev/null -w '%{{http_code}}' --max-time 5 http://127.0.0.1:{REMOTE_PORT}/healthz")
    if remote != "200":
        raise SystemExit(f"the server did not answer /healthz on 127.0.0.1:{REMOTE_PORT} (got {remote!r})")

    port = pick_port(options.port)
    if port != options.port:
        print(f"    port {options.port} is busy; using {port} instead", flush=True)

    if options.with_image_bridge:
        require_image_bridge()

    if options.show_token:
        token = read_token(options.target)
        print(f"    token: {token or 'could not be read from the server'}", flush=True)

    print(f"==> forwarding 127.0.0.1:{port} -> server 127.0.0.1:{REMOTE_PORT}", flush=True)
    command = [
        "ssh",
        "-N",
        "-o", "BatchMode=yes",
        "-o", "ServerAliveInterval=30",
        "-o", "ServerAliveCountMax=3",
        "-o", "ExitOnForwardFailure=yes",
        "-L", f"127.0.0.1:{port}:127.0.0.1:{REMOTE_PORT}",
    ]
    if options.with_image_bridge:
        command += ["-R", f"127.0.0.1:{IMAGE_BRIDGE_PORT}:127.0.0.1:{IMAGE_BRIDGE_PORT}"]
    command.append(options.target)

    tunnel = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        # An address is only printed once /healthz answers through the tunnel.
        for _ in range(20):
            time.sleep(0.4)
            if tunnel.poll() is not None:
                error = (tunnel.stderr.read() or b"").decode("utf-8", "replace").strip()
                raise SystemExit(f"the ssh tunnel exited immediately: {error or 'no output'}")
            if http_status(f"http://127.0.0.1:{port}/healthz", timeout=3) == 200:
                break
        else:
            raise SystemExit("the tunnel opened but /healthz never answered through it")

        print("", flush=True)
        print(f"  chat-agent  http://127.0.0.1:{port}", flush=True)
        if options.with_image_bridge:
            print(f"  imagebridge reverse port {IMAGE_BRIDGE_PORT} forwarded", flush=True)
        print("", flush=True)
        print("  Open that address, then paste the access token at the login prompt.", flush=True)
        print("  Press Ctrl+C to close the tunnel.", flush=True)
        print("", flush=True)

        while tunnel.poll() is None:
            time.sleep(0.5)
        print("tunnel closed.", flush=True)
        return 0
    except KeyboardInterrupt:
        print("\ntunnel closed.", flush=True)
        return 0
    finally:
        if tunnel.poll() is None:
            tunnel.terminate()
            try:
                tunnel.wait(timeout=5)
            except subprocess.TimeoutExpired:
                tunnel.kill()


if __name__ == "__main__":
    sys.exit(main())
