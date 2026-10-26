package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
	"github.com/swtormy/tgbot-cli/internal/mtp"
	"golang.org/x/term"
)

// isTerminal is a variable so tests can stand in for a person at a keyboard.
var isTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (a *app) prompter() func(label string, secret bool) (string, error) {
	rd := bufio.NewReader(a.in)
	return func(label string, secret bool) (string, error) {
		fmt.Fprintf(a.errOut, "%s: ", label)
		if secret && isTerminal(a.in) {
			b, err := term.ReadPassword(int(a.in.(*os.File).Fd()))
			fmt.Fprintln(a.errOut)
			return string(b), err
		}
		line, err := rd.ReadString('\n')
		if err != nil && line == "" {
			return "", usageErr("input ended before %q was answered", label)
		}
		return strings.TrimSpace(line), nil
	}
}

func (a *app) userLoginCmd(uf *userFlags) *cobra.Command {
	var (
		apiID          int
		apiHash, phone string
		code, telethon string
		qr, resend     bool
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in once; the session is then reused until you log out",
		Long: `Sign in to a Telegram account. Do this ONCE, as a human, in a normal terminal.
Telegram's login code goes to your Telegram app; the session is saved on this
computer and reused by every later command, so you should not need to log in
again for months. If the profile is already logged in, this does nothing.

Needs api_id and api_hash from https://my.telegram.org > API development tools
(one pair per person; pass them as flags, as TGBOT_API_ID / TGBOT_API_HASH, or
answer the prompt). They are saved with the session.

Ways to log in:
  tgbot user login                     asks for phone, code and 2-step password
  tgbot user login --qr                shows a QR code: scan it in Telegram >
                                       Settings > Devices > Link Desktop Device
  tgbot user login --telethon-session -   import a Telethon StringSession from stdin
                                       (stop using it in Telethon afterwards, or
                                       Telegram will revoke both)

No terminal available (for example a tool that cannot type)? Two steps:
  tgbot user login --phone +15551234567            sends the code
  tgbot user login --phone +15551234567 --code 12345
If the code does not arrive (it is a message from the "Telegram" chat in an app where
you are already logged in, not an SMS), wait the time shown and ask for another route:
  tgbot user login --phone +15551234567 --resend
Set TGBOT_PASSWORD for that one call if the account has a 2-step password.
The password is never saved.

Use --profile NAME to keep several accounts side by side.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.profileFor(uf, false)
			if err != nil {
				return err
			}
			_ = p.Load(a.getenv) // keeps existing api credentials; a missing config is fine
			interactive := isTerminal(a.in)
			var prompt func(string, bool) (string, error)
			if interactive {
				prompt = a.prompter()
			}
			if apiID == 0 {
				if v := a.getenv("TGBOT_API_ID"); v != "" {
					apiID, _ = strconv.Atoi(v)
				} else {
					apiID = p.Cfg.APIID
				}
			}
			if apiHash == "" {
				apiHash = firstNonEmpty(a.getenv("TGBOT_API_HASH"), p.Cfg.APIHash)
			}
			if (apiID == 0 || apiHash == "") && prompt != nil {
				fmt.Fprintln(a.errOut, "Get api_id and api_hash at https://my.telegram.org > API development tools.")
				if apiID == 0 {
					s, err := prompt("api_id", false)
					if err != nil {
						return err
					}
					apiID, _ = strconv.Atoi(s)
				}
				if apiHash == "" {
					apiHash, err = prompt("api_hash", false)
					if err != nil {
						return err
					}
				}
			}
			if telethon == "-" {
				t, err := a.readStdin()
				if err != nil {
					return err
				}
				telethon = strings.TrimSpace(t)
			}
			if telethon == "" {
				telethon = a.getenv("TGBOT_TELETHON_SESSION")
			}
			if telethon != "" {
				fmt.Fprintln(a.errOut, "Importing a Telethon session. Stop that Telethon client now: two clients on one session make Telegram revoke it.")
			}
			o := mtp.LoginOptions{
				Options: a.mtpOptions(uf), APIID: apiID, APIHash: apiHash, Phone: phone, Code: code, Resend: resend,
				Password: a.getenv("TGBOT_PASSWORD"), QR: qr, TelethonSession: telethon, Prompt: prompt,
			}
			if qr {
				if !interactive {
					return usageErr("--qr needs a terminal to show the code")
				}
				o.ShowQR = func(url string) {
					q, err := qrcode.New(url, qrcode.Low)
					if err != nil {
						fmt.Fprintln(a.errOut, url)
						return
					}
					fmt.Fprintln(a.errOut, "\nScan in Telegram > Settings > Devices > Link Desktop Device:")
					fmt.Fprintln(a.errOut, q.ToSmallString(false))
				}
			}
			if telethon != "" || code != "" || resend {
				o.Prompt = nil
			}
			ctx, stop := signalCtx(cmd)
			defer stop()
			res, err := p.Login(ctx, o)
			if err != nil {
				return a.mtpErr(err)
			}
			if res.CodeSent {
				out := map[string]any{
					"ok": true, "code_sent": true, "via": res.CodeVia,
					"hint": mtp.CodeHint(res.CodeVia, res.CodeNext, res.CodeTimeout),
					"next": fmt.Sprintf("tgbot user login --phone %s --code <the code from Telegram>", phone),
				}
				if res.CodeNext != "" {
					out["resend_via"] = res.CodeNext
					out["resend_after_seconds"] = res.CodeTimeout
				}
				return a.emitValue(out)
			}
			return a.emitValue(map[string]any{
				"ok": true, "already_logged_in": res.AlreadyLoggedIn, "profile": p.Name,
				"user":   mtp.Who{ID: res.Self.ID, Type: "user", Name: p.Cfg.Name, Username: res.Self.Username},
				"access": p.Cfg.Access,
				"next":   "Run `tgbot user status`. Writes stay blocked until you run `tgbot user access write`.",
			})
		},
	}
	f := cmd.Flags()
	f.IntVar(&apiID, "api-id", 0, "api_id from my.telegram.org (or $TGBOT_API_ID)")
	f.StringVar(&apiHash, "api-hash", "", "api_hash from my.telegram.org (or $TGBOT_API_HASH)")
	f.StringVar(&phone, "phone", "", "phone number with country code")
	f.StringVar(&code, "code", "", "login code (second step of the no-terminal login)")
	f.BoolVar(&resend, "resend", false, "ask Telegram to send the pending code another way (SMS or call); use with --phone")
	f.BoolVar(&qr, "qr", false, "log in by scanning a QR code")
	f.StringVar(&telethon, "telethon-session", "", "import a Telethon StringSession (- reads stdin)")
	return cmd
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *app) userLogoutCmd(uf *userFlags) *cobra.Command {
	var local bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "End the session on Telegram and delete it from this computer",
		Long: `Terminate the session at Telegram and delete the local files. Afterwards a
new login (with a code) is needed. Needs a terminal, so an agent cannot do it.
--local only deletes the local files and leaves the session alive on Telegram
(then end it yourself in Telegram > Settings > Devices).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isTerminal(a.in) {
				return &exitError{code: exitBlocked, kind: "blocked", err: fmt.Errorf("logout needs an interactive terminal; ask the user to run it")}
			}
			p, err := a.profileFor(uf, true)
			if err != nil {
				return err
			}
			ans, err := a.prompter()(fmt.Sprintf("Log out profile %q? Type 'logout' to confirm", p.Name), false)
			if err != nil {
				return err
			}
			if ans != "logout" {
				return usageErr("cancelled")
			}
			ctx, stop := signalCtx(cmd)
			defer stop()
			revoked, err := p.Logout(ctx, a.mtpOptions(uf), local)
			if err != nil {
				return a.mtpErr(err)
			}
			return a.emitValue(map[string]any{"ok": true, "profile": p.Name, "revoked_on_telegram": revoked})
		},
	}
	cmd.Flags().BoolVar(&local, "local", false, "only delete local files")
	return cmd
}

func (a *app) userStatusCmd(uf *userFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Who you are logged in as and how long the session will last",
		Long: `Check that the saved session works and show: the account, the access level
(read or write), when the session was created and last used, and the idle limit
after which Telegram ends an unused session (session_ttl_days, normally 180).
Using the session regularly keeps it alive.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.profileFor(uf, true)
			if err != nil {
				return err
			}
			return a.connect(cmd, uf, "", func(ctx context.Context, c *mtp.Conn) (any, error) {
				st, err := c.Status(ctx)
				if err != nil {
					return nil, err
				}
				st["dir"] = p.Dir
				return st, nil
			})
		},
	}
}

func (a *app) userAccessCmd(uf *userFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "access [read|write]",
		Short: "Show or change what this profile may do (write needs a human at a terminal)",
		Long: `Without an argument, shows the access level.

  read   (default) only reading commands work; anything that changes the account is blocked (exit 4)
  write  also allows send, edit, delete, forward, read, join, leave and non-read raw methods

Switching to write asks you to confirm and only works in an interactive terminal,
so an agent that merely runs commands cannot grant itself write access.
This is a safety rail against mistakes and prompt injection, not a sandbox:
anything that can edit files in your config directory could change it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.profileFor(uf, true)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				switch args[0] {
				case mtp.AccessRead:
				case mtp.AccessWrite:
					if !isTerminal(a.in) {
						return &exitError{code: exitBlocked, kind: "blocked", err: fmt.Errorf(
							"write access can only be granted by a person in an interactive terminal; ask the user to run `tgbot user access write` themselves")}
					}
					ans, err := a.prompter()(fmt.Sprintf("Allow tgbot to send, edit and delete as %s (profile %q)? Type 'write' to confirm", p.Cfg.Name, p.Name), false)
					if err != nil {
						return err
					}
					if ans != "write" {
						return usageErr("cancelled")
					}
				default:
					return usageErr("access must be read or write")
				}
				p.Cfg.Access = args[0]
				if err := p.Save(); err != nil {
					return runtimeErr(err)
				}
			}
			return a.emitValue(map[string]any{"profile": p.Name, "access": p.Cfg.Access})
		},
	}
}

func (a *app) userProfilesCmd(uf *userFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "profiles",
		Short: "List saved accounts on this computer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := mtp.Home(a.getenv)
			if err != nil {
				return runtimeErr(err)
			}
			entries, _ := os.ReadDir(filepath.Join(home, "user"))
			type item struct {
				Name     string `json:"profile"`
				User     string `json:"user,omitempty"`
				Username string `json:"username,omitempty"`
				Access   string `json:"access,omitempty"`
			}
			out := []item{}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				it := item{Name: e.Name()}
				if b, err := os.ReadFile(filepath.Join(home, "user", e.Name(), "config.json")); err == nil {
					var c mtp.Config
					if json.Unmarshal(b, &c) == nil {
						it.User, it.Username, it.Access = c.Name, c.Username, c.Access
					}
				}
				out = append(out, it)
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			return a.emitValue(out)
		},
	}
}
