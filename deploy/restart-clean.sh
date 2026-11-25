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

cd "/root/deploy-$release"
sh install.sh "$release" "$sha"

systemctl show -p ActiveState -p NRestarts chat-agent.service | tr '\n' ' '
echo
curl -fsS -o /dev/null -w 'healthz=%{http_code}\n' --max-time 10 http://127.0.0.1:9991/healthz
