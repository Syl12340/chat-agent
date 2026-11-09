# Renaming pocket to chat

The product has been chat-agent for a long time, but the old working name had
spread into module paths, environment variables, the systemd unit, the data
directory, the WebView bridge objects, the user-agent marker and the
documentation. This records what moved, what deliberately did not, and how to
read the layout afterwards.

## What changed

| Surface | Before | After |
|---|---|---|
| Go module | `pocket-agent` | `chat-agent` |
| Environment | `POCKET_*` | `CHAT_*` |
| Service | `pocket-agent.service` | `chat-agent.service` |
| Release root | `/opt/pocket-agent` | `/opt/chat-agent` |
| Data | `/var/lib/pocket-agent` | `/var/lib/chat-agent` |
| Config | `/etc/pocket-agent/{pocket-agent.env,transport}` | `/etc/chat-agent/{chat-agent.env,transport}` |
| Search service | `pocket-search.service` | `chat-search.service` |
| WebView bridge | `PocketAndroid*`, `pocket-native-ready` | `ChatAndroid*`, `chat-native-ready` |
| User agent | `PocketAgentAndroid/<v>` | `ChatAgentAndroid/<v>` |
| Service worker | `pocket-shell-vN` | `chat-shell-vN` |

## What deliberately did not change

`dev.pocketagent.android` stays as the Android application id.

That identifier names the installed application. Changing it does not rename the
app — it installs a *different* app alongside the old one, leaving the previous
icon behind and starting the new one with empty local state. Everything a reader
sees now says chat; this one string remains a compatibility fact.

The system accounts `pocket-agent` and `pocket-search` are left in place but no
longer used. Owned files moved to `chat-agent`/`chat-search`; deleting an account
that still owns something is a worse failure than carrying an unused one.

## Compatibility

Two things let an older client keep working:

- The old paths are symlinks: `/opt/pocket-agent`, `/var/lib/pocket-agent` and
  `/etc/pocket-agent` all point at their chat equivalents.
- The page still reads `PocketAndroid*` objects and listens for
  `pocket-native-ready`. A shell installed before the rename keeps every native
  feature instead of losing them all at once.

The service worker cache name changed, so the first load after this release
fetches a fresh shell rather than serving the previous one from cache.

## Server migration

The migration ran as a single checked script with the services stopped:

1. Back up `var/lib/pocket-agent`, `etc/pocket-agent` and both unit files.
2. Create the `chat-agent` and `chat-search` accounts.
3. Move the three directory trees.
4. Rename the environment file and rewrite its `POCKET_` prefixes to `CHAT_`.
5. Repoint path values inside the file at the new tree.
6. Move ownership to the new accounts.
7. Leave the old paths as symlinks.
8. Run `install.sh`, which installs the binary, the unit and enables the service.

One step is easy to miss: the transport directory kept its old group, so the
service started and died with `cannot read transport signing key`. Ownership of
`/etc/chat-agent/transport` has to move with the account, not just the data
directory.

Rollback is a relink and a restart; the pre-migration archive is
`/opt/chat-agent/backups/pre-chat-rename.tar.gz`.
