# mautrix-bitrix

A Matrix ↔ Bitrix24 puppeting bridge built on [mautrix-go bridgev2](https://github.com/mautrix/go),
for running as a self-hosted bridge on Beeper. Targets self-hosted ("box") Bitrix24 portals and
talks to the same internal API as the web messenger (reverse-engineered from a browser HAR).

## How it works

| Piece | Bitrix side |
|---|---|
| Login | `POST /auth/?login=yes` form (LDAP/password), CSRF token `bitrix_sessid` scraped from `/stream/` |
| Session refresh | cookies persisted; on `invalid_csrf`/`NO_AUTH` the token is re-fetched, then a password re-login |
| Real-time | `pull.config.get` → websocket `wss://<portal>/bitrix/subws/?CHANNEL_ID=…&revision=19` (text mode, `#!NGINXNMS!#{json}#!NGINXNME!#` frames) |
| Chat list | `/rest/im.recent.list.json` |
| Chat info / history | `ajax.php?action=im.v2.Chat.load`, `im.v2.Chat.Message.list`, `im.v2.Chat.Message.tail` |
| Members / users | `im.dialog.users.list`, `im.user.get` |
| Send | `im.v2.Chat.Message.send` (`fields[message]`, `fields[replyId]`, `fields[templateId]`) |
| Edit / delete | `im.v2.Chat.Message.update` (`id`, `fields[message]`), `im.v2.Chat.Message.delete` (`id`) |
| Reactions | `/rest/im.v2.Chat.Message.Reaction.add/delete.json` (like, kiss, laugh, wonder, cry, angry, facepalm) |
| Read / typing | `im.dialog.read`, `im.dialog.writing` |
| Files | download via `urlDownload`; upload raw bytes to `ajax.php?controller=disk.uf.integration.diskUploaderController&action=ui.fileuploader.upload` (chat `diskFolderId`), then `im.disk.file.commit` (`upload_id[]`, `template_id`); the message ID comes from the push echo |
| Pins | Bitrix → Matrix only (`messageParamsUpdate` `IS_PINNED`, `pinDelete`); bridgev2 has no Matrix-side pin hook |

IDs: portal = Bitrix dialog ID (`"123"` for a DM with user 123, `"chat456"` for a group),
ghost = Bitrix user ID, message = Bitrix message ID. One bridge instance serves one portal.

Supported: text with BBCode↔HTML formatting and mentions, files/images in both directions,
replies, edits, deletes, reactions, read receipts, typing, backfill, chat name/avatar/member sync.

## Running with Beeper

```sh
nix shell nixpkgs#go nixpkgs#beeper-bridge-manager
./build.sh
bbctl login                                   # your Beeper account
bbctl config --type bridgev2 sh-bitrix -o config.yaml
./mautrix-bitrix -c config.yaml               # first run fills in the `network:` section
```

Optionally set `network.default_portal_url` in `config.yaml`. Then in Beeper, open a chat with
`@sh-bitrixbot:beeper.local` and send `login` (or use the "Add account" UI).

The login password is stored in the bridge database so the bridge can re-login when the PHP
session expires. Keep `config.yaml` and the `.db` file private.

## Smoke test without Matrix

```sh
BITRIX_URL=https://portal.example.com BITRIX_LOGIN=me BITRIX_PASSWORD=… \
  go run -tags goolm ./cmd/bitrix-probe -listen 2m
```

Read-only: logs in, lists recent chats, loads a few messages and prints raw push events.

## Known gaps

- Request and push shapes were verified against Chrome HARs (Firefox-based browsers don't export
  websocket frames). Run with `logging.min_level: trace` to see raw push events.
- Uploads over 5 MB use chunked Content-Range requests, which haven't been verified yet.
- No captcha / 2FA login flow. No open lines, calls, channels/comments threads or polls.
