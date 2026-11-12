#!/bin/sh
# Stop the service, clear stale store locks, publish, and start.
#
# The application takes an exclusive lock per store on startup and does not remove
# it when killed hard. A restart during install therefore fails with "store lock
# unavailable", systemd retries, hits its start limit and gives up — the service then
# stays down even though nothing is wrong with the new build. Clearing the locks
# after the old process is gone is what makes a restart reliable.
set -eu
release=$1
sha=$2

systemctl stop chat-agent 2>/dev/null || true
for _ in 1 2 3 4 5 6 7 8 9 10; do
  pgrep -x chat-agent >/dev/null 2>&1 || break
  sleep 1
done
find /var/lib/chat-agent -name '.*.lock' -delete 2>/dev/null || true
find /var/lib/chat-agent -name '*.lock' -delete 2>/dev/null || true

cd "/root/deploy-$release"
sh install.sh "$release" "$sha"

sleep 2
systemctl reset-failed chat-agent 2>/dev/null || true
systemctl start chat-agent
sleep 6
systemctl show -p ActiveState -p NRestarts chat-agent.service | tr '\n' ' '
echo
curl -s -o /dev/null -w 'healthz=%{http_code}\n' --max-time 10 http://127.0.0.1:9991/healthz || true
