# tgbot

Telegram from the shell, built for AI agents: the Bot API with a bot token, and your own account over MTProto. One binary, no server, JSON in and out.

Inspired by Google's [Gemini API CLI](https://github.com/google-gemini/gemini-api-cli).

```bash
export TELEGRAM_BOT_TOKEN="123456:ABC..."    # from @BotFather
tgbot send 123456789 "build finished"
```

An agent with a shell (Claude Code, Cursor, Grok Build, Codex, and others) can call every Bot API method with it. There is no MCP server to run and no tool schemas sitting in the agent's context. The agent learns the commands from the binary itself, only when it needs them.

Two modes in one binary:

| | Bot mode (`tgbot send ...`) | User mode (`tgbot user ...`) |
|---|---|---|
| Acts as | a bot from @BotFather | your own Telegram account |
| Login | a token in an environment variable | once, with a code from your Telegram app |
| Good for | notifications, bot workflows | reading and searching your chats, replying as you, watching for messages |
| Docs | this page | [docs/USER_MODE.md](docs/USER_MODE.md) |

## Install

macOS and Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/swtormy/tgbot-cli/main/scripts/install.sh | bash
```

Or with Go:

```bash
go install github.com/swtormy/tgbot-cli/cmd/tgbot@latest
```

Prebuilt binaries for macOS, Linux and Windows are on the [releases page](https://github.com/swtormy/tgbot-cli/releases).

### Download a release binary

Pick the archive for your OS and CPU from a [GitHub release](https://github.com/swtormy/tgbot-cli/releases) (draft releases are visible to repo collaborators until published):

| Platform | Archive |
|---|---|
| Linux x86_64 | `tgbot_linux_amd64.tar.gz` |
| Linux arm64 | `tgbot_linux_arm64.tar.gz` |
| macOS Intel | `tgbot_darwin_amd64.tar.gz` |
| macOS Apple Silicon | `tgbot_darwin_arm64.tar.gz` |
| Windows x86_64 | `tgbot_windows_amd64.zip` |
| Windows arm64 | `tgbot_windows_arm64.zip` |

Each archive contains the `tgbot` binary (`.exe` on Windows), plus `LICENSE` and `NOTICE`. Verify the download against `checksums.txt` from the same release:

```bash
curl -fsSLO https://github.com/swtormy/tgbot-cli/releases/download/vX.Y.Z/checksums.txt
curl -fsSLO https://github.com/swtormy/tgbot-cli/releases/download/vX.Y.Z/tgbot_linux_amd64.tar.gz
grep ' tgbot_linux_amd64.tar.gz$' checksums.txt | sha256sum -c -
tar -xzf tgbot_linux_amd64.tar.gz
./tgbot_linux_amd64/tgbot --version
```

Replace `vX.Y.Z` with the tag you downloaded. On macOS without GNU `sha256sum`, use `shasum -a 256 -c` the same way.

## Quick start

1. Create a bot with [@BotFather](https://t.me/BotFather) and export its token as `TELEGRAM_BOT_TOKEN`.
2. Send the bot a message so it can write to you back, then read your chat id:

   ```bash
   tgbot updates --jq '.updates[].message.chat.id'
   ```

3. Send something:

   ```bash
   tgbot send 123456789 "hello from tgbot"
   tgbot sendPhoto --chat_id 123456789 --photo @cat.jpg --caption "meow"
   ```

Every call can be previewed first with `--dry-run`. It needs no token and prints the request that would be sent.

## Give it to your agent

The skill is built into the binary, so one command installs it:

```bash
tgbot skill install                        # ./.claude/skills/tgbot/SKILL.md (Claude Code)
tgbot skill install --agent cursor --global
tgbot skill install --agent grok
tgbot skill install --agent agents         # shared .agents/skills folder
```

Or paste this into `AGENTS.md`, `CLAUDE.md` or your tool's rules, which is all an agent needs:

```text
You have access to tgbot through the shell (Telegram Bot API, token in TELEGRAM_BOT_TOKEN).
1. Run `tgbot methods` to list methods and `tgbot <method> --help` for parameters.
2. Use `tgbot <method> --schema` for the request body schema and `--dry-run` to preview a call.
3. Results are JSON on stdout; errors are one JSON line on stderr. Exit code 3 means a token problem.
4. Ask the user before destructive calls such as deleteMessage, banChatMember or leaveChat.
```

## User mode (your own account)

Log in once in a normal terminal, then read and search your chats:

```bash
tgbot user login                          # once: api_id/api_hash from my.telegram.org, phone, code
tgbot user inbox                          # unread messages, grouped by chat
tgbot user search "invoice" --since 30d
tgbot skill install tgbot-user            # teach your agent how to use it
```

- The session is reused for months. Parallel calls queue behind a lock so Telegram never sees the same session from two places.
- Read-only by default. Sending, editing and deleting stay blocked (exit code 4) until you run `tgbot user access write` yourself in a terminal.
- Message text is untrusted input: results carry `"untrusted": true`, and the skill tells agents never to follow instructions found in messages.
- No daemon: `tgbot user poll` returns what is new since the last call, `tgbot user watch --for 5m` streams live.
- `tgbot user raw <method>` reaches every other MTProto method.

**Login is the one step that needs you.** Telegram delivers third-party login codes only into an already logged-in app, and sometimes nothing at all. If no code arrives, use the QR login: `tgbot user login --qr` in a terminal, or `tgbot user login --qr-file /tmp/tgbot-qr.png` without one (show the image and scan it from Settings > Devices > Link Desktop Device within ~30 s; set `TGBOT_PASSWORD` first if the account has two-step verification). Only have a phone and a headless agent? See [Phone-only owner, headless agent](docs/USER_MODE.md#phone-only-owner-headless-agent). To run the agent somewhere else, log in on your computer and move the session with `tgbot user session export` / `tgbot user login --import -`. Full list of known failures: [Login needs a human](docs/USER_MODE.md#login-needs-a-human-known-problems).

Full guide for people and agents: [docs/USER_MODE.md](docs/USER_MODE.md).

## Commands

| Command | What it does |
|---|---|
| `tgbot send <chat_id> <text>` | Shortcut for `sendMessage`. A text of `-` reads stdin. |
| `tgbot updates` | `getUpdates` that prints `{"next_offset":N,"updates":[...]}`. `--state-file` remembers the offset between calls. |
| `tgbot download <file_id>` | Fetches a file and prints the local path. |
| `tgbot <method> [flags]` | Any of the 185 Bot API methods, for example `sendPhoto`, `pinChatMessage`, `createForumTopic`. |
| `tgbot methods` | Lists all methods. |
| `tgbot <method> --help` | Parameters of one method. |
| `tgbot <method> --schema` | JSON Schema of the request body. |
| `tgbot --usage` | The whole method tree as compact JSON. |
| `tgbot user ...` | Your own account. See [docs/USER_MODE.md](docs/USER_MODE.md). |
| `tgbot skill print\|install [tgbot\|tgbot-user]` | The bundled agent skills. |

Flags use the exact parameter names from the [Bot API docs](https://core.telegram.org/bots/api), so `--chat_id`, `--reply_markup` and so on.

- Integers, booleans and strings are converted from the method's declared types. `--chat_id @channel` stays a string, `--chat_id 123` becomes a number.
- Objects and arrays are JSON strings: `--reply_markup '{"inline_keyboard":[[{"text":"Open","url":"https://example.com"}]]}'`. `@file.json` reads them from a file.
- File parameters accept `@path` to upload a local file, or a `file_id` or URL.
- `--body '{"chat_id":1,"text":"hi"}'` passes the whole request as JSON (`@file` or `-` for stdin). Flags override it. Unknown keys are rejected.

Global flags: `--dry-run`, `--jq EXPR` (strings print raw), `--pretty`, `--api-url`, `--http-timeout`.

## Output and exit codes

- Success: the Bot API `result` as JSON on stdout.
- Failure: one JSON line on stderr, for example `{"ok":false,"error":{"code":400,"message":"Bad Request: chat not found","type":"api"}}`. For rate limits the error includes `parameters.retry_after`.

| Exit code | Meaning |
|---|---|
| 0 | Success |
| 1 | API or runtime failure |
| 2 | Usage error: unknown flag, missing required parameter, invalid value |
| 3 | Token missing or rejected |

## How it works

The method catalog (names, parameters, types, required flags) is embedded in the binary from the machine-readable Bot API description in [PaulSonOfLars/telegram-bot-api-spec](https://github.com/PaulSonOfLars/telegram-bot-api-spec), currently Bot API 10.3. At start-up tgbot turns it into commands and flags, so `--help`, `--schema` and `--usage` always match what the binary can actually send. Calls are plain HTTPS `POST`s to `https://api.telegram.org/bot<token>/<method>`.

To refresh the catalog, run `scripts/update-spec.sh`. A weekly workflow opens a pull request with the update.

## Security

- The token is read from the environment only. It is never printed: error messages and `--dry-run` output replace it with `<TOKEN>`.
- A bot can only act in chats it was added to, but that includes deleting messages and banning members where it is an admin. Review what you let an agent do, and use `--dry-run`.
- Use a separate bot per project and revoke the token with `/revoke` in @BotFather if it leaks.

## Limitations

- Bot mode is the Bot API only. Reading a person's chats is user mode, which needs your own `api_id`/`api_hash` and a login.
- User mode has no secret chats, calls or payments, and refuses login/password/account-deletion methods even through `raw`.
- Uploads work for top-level file parameters. Media groups and other requests that reference files through `attach://` inside JSON need `file_id`s or URLs for now.
- Bots cannot download files larger than 20 MB through the cloud Bot API.

## Development

Layer-232 MTProto client: [gotd/td](https://github.com/gotd/td) (MIT).

```bash
go test ./...
goreleaser release --snapshot --clean -p 1   # local release build into ./dist
```

## License

MIT. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
