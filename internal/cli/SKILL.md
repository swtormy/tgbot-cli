---
name: tgbot
description: Send Telegram messages, photos and files, read bot updates, and call any Telegram Bot API method through the tgbot CLI. Use when the task involves Telegram, a Telegram bot, notifications to a chat or channel, or reading messages sent to a bot.
---

# tgbot: Telegram Bot API from the shell

`tgbot` is a CLI for the Telegram Bot API. The token comes from the environment, output is JSON.

## Before you start

- The token must be in `TELEGRAM_BOT_TOKEN`. Never print it, never put it on a command line.
- Check the setup without a token: `tgbot send 1 hi --dry-run` shows the request that would be sent.
- Bots only see chats they were added to. A bot cannot message a user who has not started it.

## Discover instead of guessing

1. `tgbot methods` lists every Bot API method.
2. `tgbot <method> --help` shows its parameters. Names are exactly the Bot API names (snake_case).
3. `tgbot <method> --schema` prints the JSON Schema of the request body.
4. Add `--dry-run` to preview any call before sending.

## Common calls

```bash
tgbot send 123456789 "text"                       # sendMessage shortcut; "-" reads text from stdin
tgbot send @channelname "<b>hi</b>" --parse_mode HTML
tgbot sendPhoto --chat_id 123456789 --photo @./pic.jpg --caption "caption"
tgbot sendDocument --chat_id 123456789 --document @./report.pdf
tgbot updates --state-file ~/.tgbot-offset        # new updates only, offset saved between calls
tgbot updates --timeout 30 --jq '.updates[].message.text'
tgbot download <file_id> --out ./downloads/       # prints the saved path
tgbot sendMessage --chat_id 123 --text hi --reply_markup '{"inline_keyboard":[[{"text":"Open","url":"https://example.com"}]]}'
```

- File parameters: `@path` uploads a local file; a plain value is a `file_id` or URL.
- Object and array parameters are JSON strings; `@file.json` reads them from a file.
- `--jq EXPR` filters the result. Strings print raw, so `--jq '.message_id'` is safe in `$(...)`.

## Output and errors

- Success: the Bot API `result` as JSON on stdout.
- Failure: one JSON line on stderr, `{"ok":false,"error":{"type":...,"message":...}}`.
- Exit codes: 0 ok, 1 API or runtime failure, 2 usage error, 3 token missing or rejected.
- On a 429 error read `parameters.retry_after` and wait that many seconds.

## Ask before

Methods that change other people's state or cannot be undone: `deleteMessage`, `banChatMember`, `leaveChat`, `setChatPermissions`, `deleteWebhook`, `logOut`, `close`. Preview with `--dry-run` and confirm with the user first.
