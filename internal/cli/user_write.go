package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/swtormy/tgbot-cli/internal/mtp"
)

const writeNote = "Needs write access: the user must run `tgbot user access write` in their own terminal once."

func (a *app) userSendCmd(uf *userFlags) *cobra.Command {
	var (
		replyTo                   int
		silent, noPreview, asFile bool
		parse, file, schedule     string
	)
	cmd := &cobra.Command{
		Use:   "send <peer> [text]",
		Short: "Send a message or file as yourself",
		Long: `Send a message as the logged-in user. A text of "-" reads stdin.
Attach a photo, video or document with --file (the text becomes its caption).
--parse html|md turns <b>bold</b> or **bold** into formatting.
--schedule +30m (or a date-time) schedules the message instead of sending it now.

` + writeNote + `
Sending is visible to other people and cannot be taken back silently: preview with --dry-run.`,
		Example: `  tgbot user send @alice "running late"
  tgbot user send "Team chat" --file report.pdf "weekly report"
  echo "deploy done" | tgbot user send -1001234567890 - --reply-to 4521`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := ""
			if len(args) == 2 {
				text = args[1]
				if text == "-" {
					t, err := a.readStdin()
					if err != nil {
						return err
					}
					text = strings.TrimRight(t, "\r\n")
				}
			}
			if text == "" && file == "" {
				return usageErr("nothing to send: give text or --file")
			}
			var when time.Time
			if schedule != "" {
				t, err := parseFuture(schedule, time.Now())
				if err != nil {
					return err
				}
				when = t
			}
			if a.dryRun {
				return a.planWrite(uf, "send", map[string]any{"peer": args[0], "text": text, "file": file, "reply_to": replyTo, "schedule": schedule})
			}
			return a.connect(cmd, uf, "sending a message", func(ctx context.Context, c *mtp.Conn) (any, error) {
				p, err := c.Resolve(ctx, args[0])
				if err != nil {
					return nil, err
				}
				return c.Send(ctx, p, mtp.SendOpts{Text: text, ReplyTo: replyTo, Silent: silent, NoPreview: noPreview, Parse: parse, File: file, AsFile: asFile, Schedule: when})
			})
		},
	}
	f := cmd.Flags()
	f.IntVar(&replyTo, "reply-to", 0, "reply to this message id")
	f.BoolVar(&silent, "silent", false, "send without a notification sound")
	f.BoolVar(&noPreview, "no-preview", false, "do not expand link previews")
	f.StringVar(&parse, "parse", "", "formatting: html or md")
	f.StringVar(&file, "file", "", "path of a photo, video or document to attach")
	f.BoolVar(&asFile, "as-file", false, "send images and videos as plain files")
	f.StringVar(&schedule, "schedule", "", "send later: +30m, +2h or 2026-10-30T09:00")
	return cmd
}

func (a *app) userEditCmd(uf *userFlags) *cobra.Command {
	var parse string
	cmd := &cobra.Command{
		Use:   "edit <peer> <message_id> <text>",
		Short: "Edit one of your own messages",
		Long:  "Replace the text of a message you sent. A text of \"-\" reads stdin.\n\n" + writeNote,
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := atoiList(args[1:2])
			if err != nil {
				return err
			}
			text := args[2]
			if text == "-" {
				t, err := a.readStdin()
				if err != nil {
					return err
				}
				text = strings.TrimRight(t, "\r\n")
			}
			if a.dryRun {
				return a.planWrite(uf, "edit", map[string]any{"peer": args[0], "id": ids[0], "text": text})
			}
			return a.connect(cmd, uf, "editing a message", func(ctx context.Context, c *mtp.Conn) (any, error) {
				p, err := c.Resolve(ctx, args[0])
				if err != nil {
					return nil, err
				}
				return c.Edit(ctx, p, ids[0], text, parse)
			})
		},
	}
	cmd.Flags().StringVar(&parse, "parse", "", "formatting: html or md")
	return cmd
}

func (a *app) userDeleteCmd(uf *userFlags) *cobra.Command {
	var forMe bool
	cmd := &cobra.Command{
		Use:   "delete <peer> <message_id>...",
		Short: "Delete messages (for everyone by default)",
		Long: `Delete messages. In private chats and groups they disappear for everyone unless
--for-me is given. In channels and supergroups deletion is always for everyone.
This cannot be undone. Ask the user before deleting.

` + writeNote,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := atoiList(args[1:])
			if err != nil {
				return err
			}
			if a.dryRun {
				return a.planWrite(uf, "delete", map[string]any{"peer": args[0], "ids": ids, "for_everyone": !forMe})
			}
			return a.connect(cmd, uf, "deleting messages", func(ctx context.Context, c *mtp.Conn) (any, error) {
				p, err := c.Resolve(ctx, args[0])
				if err != nil {
					return nil, err
				}
				if err := c.Delete(ctx, p, ids, !forMe); err != nil {
					return nil, err
				}
				return map[string]any{"deleted": ids}, nil
			})
		},
	}
	cmd.Flags().BoolVar(&forMe, "for-me", false, "delete only on your side")
	return cmd
}

func (a *app) userForwardCmd(uf *userFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "forward <from_peer> <to_peer> <message_id>...",
		Short: "Forward messages to another chat",
		Long:  "Forward messages, keeping the original sender label.\n\n" + writeNote,
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := atoiList(args[2:])
			if err != nil {
				return err
			}
			if a.dryRun {
				return a.planWrite(uf, "forward", map[string]any{"from": args[0], "to": args[1], "ids": ids})
			}
			return a.connect(cmd, uf, "forwarding messages", func(ctx context.Context, c *mtp.Conn) (any, error) {
				from, err := c.Resolve(ctx, args[0])
				if err != nil {
					return nil, err
				}
				to, err := c.Resolve(ctx, args[1])
				if err != nil {
					return nil, err
				}
				if err := c.Forward(ctx, from, to, ids); err != nil {
					return nil, err
				}
				return map[string]any{"forwarded": ids, "to": to.Chat()}, nil
			})
		},
	}
}

func (a *app) userReadCmd(uf *userFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "read <peer>",
		Short: "Mark a chat as read",
		Long:  "Mark every message of a chat as read. The other side sees the read receipt.\n\n" + writeNote,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.dryRun {
				return a.planWrite(uf, "read", map[string]any{"peer": args[0]})
			}
			return a.connect(cmd, uf, "marking a chat as read", func(ctx context.Context, c *mtp.Conn) (any, error) {
				p, err := c.Resolve(ctx, args[0])
				if err != nil {
					return nil, err
				}
				if err := c.MarkRead(ctx, p, 0); err != nil {
					return nil, err
				}
				return map[string]any{"read": p.Chat()}, nil
			})
		},
	}
}

func (a *app) userJoinCmd(uf *userFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "join <@channel | t.me/+invite>",
		Short: "Join a public channel/group or an invite link",
		Long:  "Join by @username, t.me/username or a t.me/+hash invite link.\n\n" + writeNote,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.dryRun {
				return a.planWrite(uf, "join", map[string]any{"target": args[0]})
			}
			return a.connect(cmd, uf, "joining a chat", func(ctx context.Context, c *mtp.Conn) (any, error) {
				w, err := c.Join(ctx, args[0])
				if err != nil {
					return nil, err
				}
				return map[string]any{"joined": w}, nil
			})
		},
	}
}

func (a *app) userLeaveCmd(uf *userFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "leave <peer>",
		Short: "Leave a group, supergroup or channel",
		Long:  "Leave a chat. Ask the user first.\n\n" + writeNote,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.dryRun {
				return a.planWrite(uf, "leave", map[string]any{"peer": args[0]})
			}
			return a.connect(cmd, uf, "leaving a chat", func(ctx context.Context, c *mtp.Conn) (any, error) {
				p, err := c.Resolve(ctx, args[0])
				if err != nil {
					return nil, err
				}
				if err := c.Leave(ctx, p); err != nil {
					return nil, err
				}
				return map[string]any{"left": p.Chat()}, nil
			})
		},
	}
}

func (a *app) userRawCmd(uf *userFlags) *cobra.Command {
	var body string
	cmd := &cobra.Command{
		Use:   "raw <method> [json]",
		Short: "Call any MTProto method (about 800) with a JSON body",
		Long: `Call any MTProto method by name. The parameters are a JSON object; the second
argument can also be @file.json or - for stdin (or use --body).

  tgbot user raw messages.getHistory '{"peer":{"@peer":"@durov"},"limit":3}'

Discover methods and parameters from the binary itself:
  tgbot user raw list [prefix]        e.g. list messages.
  tgbot user raw describe <name>      a method (parameters) or a type (constructors)

Rules for the JSON:
  - Objects need "@type" with the constructor name, e.g. {"@type":"inputPeerSelf"}.
    'describe <Type>' lists the constructors. A type with only one constructor
    does not need "@type".
  - Shorthands resolve names for you: {"@peer":"@name or id"}, {"@user":"..."},
    {"@channel":"..."} become the right Input* object.
  - Omitted optional parameters stay unset. Omitted numbers, strings and lists
    default to 0, "" and []. random_id is filled in automatically.
  - bytes are base64 strings; int128/int256 are hex strings.
Results are JSON with "@type" on every object.

Access: methods named get*/search*/resolve*/check* are reads. Everything else changes
the account and needs write access. Some methods are never allowed (login/logout,
password and phone changes, account deletion, payments, secret chats).
Use --dry-run to validate a call and see its class without sending it.`,
		Example: `  tgbot user raw list contacts.
  tgbot user raw describe messages.search
  tgbot user raw users.getFullUser '{"id":{"@user":"@durov"}}' --jq '.full_user.about'`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw := body
			if len(args) == 2 {
				raw = args[1]
			}
			switch {
			case raw == "-":
				t, err := a.readStdin()
				if err != nil {
					return err
				}
				raw = t
			case strings.HasPrefix(raw, "@"):
				b, err := os.ReadFile(strings.TrimPrefix(raw, "@"))
				if err != nil {
					return usageErr("%v", err)
				}
				raw = string(b)
			}
			params, err := mtp.ParseParams([]byte(raw))
			if err != nil {
				return a.mtpErr(err)
			}
			plan, err := mtp.PlanRaw(args[0], params, mtp.PlaceholderResolver)
			if err != nil {
				return a.mtpErr(err)
			}
			if a.dryRun {
				return a.emitValue(map[string]any{"dry_run": true, "method": plan.Method, "class": plan.Class, "result": plan.Result, "params": plan.Params})
			}
			need := ""
			if plan.Class == mtp.ClassWrite {
				need = "`raw " + plan.Method + "`"
			}
			return a.connect(cmd, uf, need, func(ctx context.Context, c *mtp.Conn) (any, error) {
				return c.Raw(ctx, args[0], params)
			})
		},
	}
	cmd.Flags().StringVar(&body, "body", "", "JSON parameters (@file or - for stdin)")
	cmd.AddCommand(&cobra.Command{
		Use:   "list [prefix]",
		Short: "List MTProto method names, optionally by prefix (e.g. messages.)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := ""
			if len(args) == 1 {
				prefix = args[0]
			}
			names := mtp.MethodNames(prefix)
			type item struct {
				Method string `json:"method"`
				Class  string `json:"class"`
			}
			out := make([]item, 0, len(names))
			for _, n := range names {
				out = append(out, item{n, mtp.Classify(n)})
			}
			return a.emitValue(map[string]any{"layer": mtp.Layer(), "count": len(out), "methods": out})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "describe <method|Type|constructor>",
		Short: "Show the parameters of a method, or the constructors of a type",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := mtp.Describe(args[0])
			if err != nil {
				return a.mtpErr(err)
			}
			if _, isMethod := d["method"]; isMethod {
				d["class"] = mtp.Classify(args[0])
			}
			b, _ := json.Marshal(d)
			return a.emit(b)
		},
	})
	return cmd
}
