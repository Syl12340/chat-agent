# SPDX-License-Identifier: AGPL-3.0-or-later
"""Transport-only AstrBot bridge for chat-agent.

This plugin deliberately stops AstrBot's local LLM pipeline for admitted
messages. chat-agent remains the sole owner of conversations, memory, tools and
model calls; AstrBot only normalizes and delivers platform messages.
"""

from __future__ import annotations

import asyncio
import hashlib
import hmac
import json
import time
from datetime import datetime, timezone
from sys import maxsize
from urllib.error import HTTPError
from urllib.parse import quote
from urllib.request import Request, urlopen

from astrbot.api import AstrBotConfig, logger
from astrbot.api.event import AstrMessageEvent, MessageChain, filter
from astrbot.api.star import Context, Star


class Main(Star):
    def __init__(self, context: Context, config: AstrBotConfig):
        super().__init__(context)
        self.context = context
        self.config = config
        self._closing = False
        self._poll_task = None
        if self.config.get("enabled", False):
            self._poll_task = asyncio.create_task(self._poll_deliveries())

    def _settings(self):
        base = str(self.config.get("base_url", "")).rstrip("/")
        secret = str(self.config.get("secret", ""))
        if not base.startswith(("http://127.0.0.1:", "http://localhost:", "https://")):
            raise ValueError("base_url must be loopback HTTP or HTTPS")
        if len(secret) < 32:
            raise ValueError("gateway secret must contain at least 32 characters")
        return base, secret

    @staticmethod
    def _signature(secret: str, body: bytes) -> str:
        return "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()

    def _request(self, method: str, path: str, payload=None, pull=False):
        base, secret = self._settings()
        body = b"" if payload is None else json.dumps(payload, ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode()
        headers = {"Accept": "application/json"}
        if pull:
            headers["X-Chat-Agent-Gateway-Token"] = secret
        else:
            headers["Content-Type"] = "application/json"
            headers["X-Chat-Agent-Signature"] = self._signature(secret, body)
        request = Request(base + path, data=body if method != "GET" else None, headers=headers, method=method)
        try:
            with urlopen(request, timeout=15) as response:
                raw = response.read(256 * 1024)
                return response.status, json.loads(raw) if raw else None
        except HTTPError as error:
            raw = error.read(256 * 1024)
            try:
                value = json.loads(raw) if raw else None
            except Exception:
                value = None
            return error.code, value

    @filter.event_message_type(filter.EventMessageType.ALL, priority=maxsize)
    async def forward_to_chat_agent(self, event: AstrMessageEvent):
        if not self.config.get("enabled", False):
            return
        platform = str(event.get_platform_name())
        allowed = {str(item) for item in self.config.get("allowed_platforms", [])}
        if platform not in allowed:
            return
        kind = "group" if event.get_group_id() else "private"
        if kind == "group" and not self.config.get("allow_groups", False):
            return
        text = str(event.get_message_str() or "").strip()
        if not text:
            return

        # Claim response ownership before the network call. If the response is
        # lost after CA accepted the event, falling through to AstrBot's LLM
        # would create two independent assistants in one conversation.
        event.stop_event()
        message = event.message_obj
        timestamp = int(getattr(message, "timestamp", 0) or time.time())
        message_id = str(getattr(message, "message_id", "") or "").strip()
        if not message_id:
            stable = "\0".join((event.unified_msg_origin, event.get_sender_id(), str(timestamp), text))
            message_id = "fallback-" + hashlib.sha256(stable.encode()).hexdigest()
        payload = {
            "version": 1,
            "platform": platform,
            "account_id": f"{event.get_platform_id()}:{event.get_self_id()}",
            "conversation_kind": kind,
            "conversation_id": str(event.unified_msg_origin),
            "sender_id": str(event.get_sender_id()),
            "sender_name": str(event.get_sender_name() or ""),
            "message_id": message_id,
            "text": text,
            "occurred_at": datetime.fromtimestamp(timestamp, timezone.utc).isoformat().replace("+00:00", "Z"),
        }
        try:
            status, response = await asyncio.to_thread(self._request, "POST", "/hooks/astrbot/v1/events", payload)
            reply = response.get("reply") if isinstance(response, dict) else None
            if reply:
                yield event.plain_result(str(reply))
            elif status not in (202, 204):
                yield event.plain_result("chat-agent 暂时没有接收这条消息，请稍后查看应用内状态。")
        except Exception as error:
            logger.error(f"chat-agent gateway submit failed: {type(error).__name__}")
            yield event.plain_result("chat-agent 网关暂时不可用；为避免重复回复，本条没有转交 AstrBot 内置模型。")

    async def _poll_deliveries(self):
        interval = max(250, min(5000, int(self.config.get("poll_interval_ms", 800)))) / 1000
        while not self._closing:
            try:
                status, delivery = await asyncio.to_thread(self._request, "GET", "/hooks/astrbot/v1/deliveries/next", None, True)
                if status == 200 and isinstance(delivery, dict):
                    await self._deliver(delivery)
                elif status not in (204,):
                    await asyncio.sleep(max(interval, 2))
                    continue
            except asyncio.CancelledError:
                return
            except Exception as error:
                logger.error(f"chat-agent gateway poll failed: {type(error).__name__}")
                await asyncio.sleep(max(interval, 2))
                continue
            await asyncio.sleep(interval)

    async def _deliver(self, delivery):
        delivery_id = str(delivery.get("delivery_id", ""))
        target = str(delivery.get("target", ""))
        messages = delivery.get("messages", [])
        state = "sent"
        try:
            if not delivery_id or not target or not isinstance(messages, list) or not messages:
                raise ValueError("invalid delivery")
            for text in messages:
                if not isinstance(text, str) or not text.strip():
                    raise ValueError("invalid delivery message")
                accepted = await self.context.send_message(target, MessageChain().message(text))
                if accepted is False:
                    state = "failed"
                    break
        except Exception as error:
            logger.error(f"chat-agent gateway delivery uncertain: {type(error).__name__}")
            state = "unknown"
        if delivery_id:
            try:
                await asyncio.to_thread(self._request, "POST", f"/hooks/astrbot/v1/deliveries/{quote(delivery_id)}/ack", {"delivery_id": delivery_id, "state": state})
            except Exception as error:
                logger.error(f"chat-agent gateway ack failed: {type(error).__name__}")

    async def terminate(self):
        self._closing = True
        if self._poll_task is not None:
            self._poll_task.cancel()
            try:
                await self._poll_task
            except asyncio.CancelledError:
                pass
