# Repo rules

This bridge is used against a **corporate** Bitrix24 portal: a real work account, with chats that
belong to colleagues. Treat it with care.

## Talking to the live portal

- **Read-only by default.** `cmd/bitrix-probe` and anything else that touches the portal may only
  read, unless the user has approved a specific write action (send, edit, delete, react, upload,
  read-marking) for a specific chat. Approval for one test doesn't carry over to the next.
- Write tests go to the user's own self-chat or a chat the user names, never to group chats.
- Keep traffic low: no bulk crawling of chats, users or history beyond what the bridge needs,
  no load tests, no parallel hammering of `ajax.php` / `/rest/`.
- Never probe for vulnerabilities, other users' data, admin endpoints or anything outside the
  messenger the web client already uses.
- Credentials: never print, log or echo the password, cookies (`PHPSESSID`, `*_UIDD`, `*_LOGIN`),
  `bitrix_sessid` or push channel IDs. Pass them through environment variables only.

## Data hygiene

- **Never commit** HARs, `config.yaml`, `*.db*`, `logs/`, probe output or binaries
  (see `.gitignore`). HARs contain the password in cleartext.
- No real data in code, tests, fixtures, commit messages or docs: no colleague names, message
  text, phone numbers, real user/chat/message IDs or file names. Use placeholders
  (`Иванов Иван`, user `42`, `chat456`) when turning captured payloads into test fixtures.
- Before every commit, scan the diff for secrets and real data:
  `git diff --cached | grep -nE 'sessid|token|PASSWORD|PHPSESSID|UIDD|<portal-domain>'`.
- The repo is public: anything committed is visible to everyone, forever.

## Environments

- **prod**: Beeper registration `sh-bitrix`, deployed to the k3s cluster from the `nix` repo
  (`kubernetes/apps/`). It's logged in to the corporate account. Don't re-register it
  or run it anywhere else: two copies on one registration fight over the Beeper websocket.
- **dev**: registration `sh-bitrix-dev`, run locally with `./dev.sh start|stop|restart|status|logs`.
  Its config and DB live in `dev/` (gitignored). Keep it **logged out** of the corporate Bitrix
  account while prod runs; two sessions on one account fight over the push channel and duplicate
  every chat in Beeper. Log in only briefly, for a specific test.

## Building

- Go isn't installed globally: `nix shell nixpkgs#go -c ./build.sh` (uses the `goolm` tag).
- `nix shell nixpkgs#go -c go test -tags goolm ./...` before committing.
- Verify Bitrix API shapes against Chrome HARs (Firefox-based browsers drop websocket frames);
  Bitrix serializes empty PHP arrays as `[]`, so map-typed fields must use `bitrix.PHPMap`.
