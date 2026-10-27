package mtp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// LoginOptions drive an interactive login. The CLI supplies the prompts, so
// this package never touches the terminal.
type LoginOptions struct {
	Options
	APIID           int
	APIHash         string
	Phone           string
	QR              bool
	TelethonSession string
	TgbotSession    string // bundle made by `tgbot user session export`
	Code            string // non-interactive step 2
	Resend          bool   // ask Telegram to deliver the pending code another way (SMS or call)
	Password        string // 2FA password, only for this call
	Prompt          func(label string, secret bool) (string, error)
	ShowQR          func(url string)
}

type LoginResult struct {
	AlreadyLoggedIn bool
	CodeSent        bool
	CodeVia         string
	CodeNext        string // how a resend would be delivered, if Telegram allows one
	CodeTimeout     int    // seconds before a resend is allowed
	Self            *tg.User
}

type pendingLogin struct {
	Phone string    `json:"phone"`
	Hash  string    `json:"hash"`
	At    time.Time `json:"at"`
}

func (p *Profile) pendingPath() string { return filepath.Join(p.Dir, "login_pending.json") }

type prompter struct {
	o     LoginOptions
	phone string
	save  func(phone, hash string)
}

// codeKind names how Telegram delivers a code: app, sms, call, ...
func codeKind(t any) string {
	n := fmt.Sprintf("%T", t)
	n = strings.TrimPrefix(n, "*tg.AuthSentCodeType")
	n = strings.TrimPrefix(n, "*tg.AuthCodeType")
	return strings.ToLower(n)
}

func sentInfo(s *tg.AuthSentCode) (via, next string, timeout int) {
	via = codeKind(s.Type)
	if n, ok := s.GetNextType(); ok {
		next = codeKind(n)
	}
	timeout, _ = s.GetTimeout()
	return
}

// CodeHint tells a person where to look for the code.
func CodeHint(via, next string, timeout int) string {
	var h string
	switch via {
	case "app":
		h = "The code is a message from the 'Telegram' chat inside the Telegram app on a device where this account is already logged in. It is not an SMS."
	case "sms", "fragmentsms", "firebasesms", "smsword", "smsphrase":
		h = "The code comes by SMS."
	case "call":
		h = "Telegram will phone you and read the code out."
	case "flashcall", "missedcall":
		h = "Telegram will call from a number whose last digits are the code."
	case "emailcode":
		h = "The code was sent to the e-mail address linked to the account."
	default:
		h = "Telegram sent the code (" + via + ")."
	}
	if next != "" {
		h += fmt.Sprintf(" If nothing arrives, wait %ds and run the same command with --resend to get it by %s.", timeout, next)
	} else if via == "app" {
		h += " Telegram offers no other delivery. If the message never shows up (Telegram sometimes delivers nothing to third-party clients), log in on a computer with a terminal: `tgbot user login --qr` (scan from Settings > Devices > Link Desktop Device), then move the session here with `tgbot user session export` and `tgbot user login --import -`."
	}
	return h
}

func (a *prompter) Phone(ctx context.Context) (string, error) {
	if a.phone != "" {
		return a.phone, nil
	}
	ph, err := a.o.Prompt("Phone number (with country code, e.g. +15551234567)", false)
	a.phone = ph
	return ph, err
}

func (a *prompter) Code(ctx context.Context, sent *tg.AuthSentCode) (string, error) {
	via, next, timeout := sentInfo(sent)
	if a.save != nil {
		a.save(a.phone, sent.PhoneCodeHash)
	}
	fmt.Fprintln(a.o.Stderr, CodeHint(via, next, timeout))
	return a.o.Prompt("Login code", false)
}

func (a *prompter) Password(ctx context.Context) (string, error) {
	return a.o.Prompt("Two-step verification password (not stored anywhere)", true)
}

func (a *prompter) AcceptTermsOfService(ctx context.Context, tos tg.HelpTermsOfService) error {
	return fmt.Errorf("this phone number has no Telegram account yet; create one in the official app first")
}

func (a *prompter) SignUp(ctx context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("sign-up is not supported")
}

func (p *Profile) savePending(phone, hash string) error {
	pb, _ := json.Marshal(pendingLogin{Phone: phone, Hash: hash, At: time.Now().UTC()})
	return writeAtomic(p.pendingPath(), pb)
}

func (p *Profile) readPending(phone string) (pendingLogin, error) {
	var pend pendingLogin
	pb, err := os.ReadFile(p.pendingPath())
	if err != nil || json.Unmarshal(pb, &pend) != nil || pend.Phone != phone {
		return pend, usagef("no code was requested for %s: run `tgbot user login --phone %s` first", phone, phone)
	}
	return pend, nil
}

// Login signs in and stores the session. If the profile already has a working
// session it does nothing: a new login is never needed while the key is valid.
func (p *Profile) Login(ctx context.Context, o LoginOptions) (*LoginResult, error) {
	o.defaults()
	if o.APIID == 0 || o.APIHash == "" {
		return nil, usagef("api_id and api_hash are required (get them at https://my.telegram.org > API development tools)")
	}
	if o.Prompt == nil && o.Phone == "" && !o.QR && o.TelethonSession == "" && o.TgbotSession == "" {
		return nil, usagef("without a terminal, login runs in two steps: `tgbot user login --phone +1555...` sends the code, then add `--code 12345`")
	}
	p.Cfg.APIID, p.Cfg.APIHash = o.APIID, o.APIHash
	unlock, err := p.lock(ctx, o.LockTimeout)
	if err != nil {
		return nil, err
	}
	defer unlock()

	if o.TelethonSession != "" {
		data, err := session.TelethonSession(strings.TrimSpace(o.TelethonSession))
		if err != nil {
			return nil, usagef("not a valid Telethon string session: %v", err)
		}
		if p.Exists() {
			return nil, usagef("profile %q already has a session; use another --profile or run `tgbot user logout` first", p.Name)
		}
		if err := (&session.Loader{Storage: &fileStorage{path: p.SessionPath()}}).Save(ctx, data); err != nil {
			return nil, runtimeErr(err)
		}
	}

	if o.TgbotSession != "" {
		_, data, err := ParseSessionBundle(o.TgbotSession)
		if err != nil {
			return nil, err
		}
		if p.Exists() {
			return nil, usagef("profile %q already has a session; use another --profile or run `tgbot user logout` first", p.Name)
		}
		if err := (&fileStorage{path: p.SessionPath()}).StoreSession(ctx, data); err != nil {
			return nil, runtimeErr(err)
		}
	}
	imported := o.TelethonSession != "" || o.TgbotSession != ""

	if o.Resend && o.Prompt == nil && o.Code == "" {
		if _, err := p.readPending(o.Phone); err != nil {
			return nil, err
		}
	}

	dispatcher := tg.NewUpdateDispatcher()
	var handler telegram.UpdateHandler
	if o.QR {
		handler = dispatcher
	}
	res := &LoginResult{}
	err = p.runClient(ctx, o.Options, handler, func(ctx context.Context, client *telegram.Client) error {
		st, err := client.Auth().Status(ctx)
		if err != nil {
			return wrapRPC(err)
		}
		if st.Authorized {
			res.AlreadyLoggedIn, res.Self = !imported, st.User
			return nil
		}
		if imported {
			return &Error{Kind: KindAuth, Message: "Telegram does not accept the imported session (it was logged out or revoked, or it is in use elsewhere)"}
		}
		if o.QR {
			if o.ShowQR == nil {
				return usagef("--qr needs a terminal")
			}
			loggedIn := qrlogin.OnLoginToken(dispatcher)
			_, err := client.QR().Auth(ctx, loggedIn, func(ctx context.Context, t qrlogin.Token) error {
				o.ShowQR(t.URL())
				return nil
			})
			if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
				pw := o.Password
				if pw == "" && o.Prompt != nil {
					var perr error
					if pw, perr = o.Prompt("Two-step verification password (not stored anywhere)", true); perr != nil {
						return perr
					}
				}
				if pw == "" {
					return usagef("this account has two-step verification: set TGBOT_PASSWORD for this run")
				}
				_, err = client.Auth().Password(ctx, pw)
			}
			if err != nil {
				return wrapRPC(err)
			}
		} else if o.Prompt != nil {
			flow := auth.NewFlow(&prompter{o: o, phone: o.Phone, save: func(ph, h string) { _ = p.savePending(ph, h) }}, auth.SendCodeOptions{})
			if err := client.Auth().IfNecessary(ctx, flow); err != nil {
				return wrapRPC(err)
			}
		} else if o.Code == "" {
			var sent tg.AuthSentCodeClass
			if o.Resend {
				pend, perr := p.readPending(o.Phone)
				if perr != nil {
					return perr
				}
				sent, err = client.Auth().ResendCode(ctx, o.Phone, pend.Hash)
				if tgerr.Is(err, "SEND_CODE_UNAVAILABLE") {
					return usagef("Telegram has no other way to deliver the code (all methods were used). Wait a few minutes and start over without --resend, or try `--qr` in a terminal")
				}
			} else {
				sent, err = client.Auth().SendCode(ctx, o.Phone, auth.SendCodeOptions{})
			}
			if err != nil {
				return wrapRPC(err)
			}
			s, ok := sent.(*tg.AuthSentCode)
			if !ok {
				return &Error{Kind: KindAuth, Message: "Telegram did not send a code (unexpected response)"}
			}
			if err := p.savePending(o.Phone, s.PhoneCodeHash); err != nil {
				return runtimeErr(err)
			}
			res.CodeSent = true
			res.CodeVia, res.CodeNext, res.CodeTimeout = sentInfo(s)
			return nil
		} else {
			pend, perr := p.readPending(o.Phone)
			if perr != nil {
				return perr
			}
			_, err = client.Auth().SignIn(ctx, o.Phone, strings.TrimSpace(o.Code), pend.Hash)
			if errors.Is(err, auth.ErrPasswordAuthNeeded) {
				if o.Password == "" {
					return &Error{Kind: KindAuth, Message: "this account has two-step verification: set the password in the TGBOT_PASSWORD environment variable for this one call and repeat the command with the same --code"}
				}
				_, err = client.Auth().Password(ctx, o.Password)
			}
			if err != nil {
				return wrapRPC(err)
			}
			_ = os.Remove(p.pendingPath())
		}
		st, err = client.Auth().Status(ctx)
		if err != nil {
			return wrapRPC(err)
		}
		res.Self = st.User
		return nil
	})
	if err != nil {
		return nil, runtimeErr(err)
	}
	if res.CodeSent {
		p.Cfg.CreatedAt = time.Now().UTC()
		if err := p.Save(); err != nil {
			return nil, runtimeErr(err)
		}
		return res, nil
	}
	if res.Self == nil {
		return nil, &Error{Kind: KindAuth, Message: "login did not complete"}
	}
	p.Cfg.UserID, p.Cfg.Username, p.Cfg.Name = res.Self.ID, res.Self.Username, userName(res.Self)
	if p.Cfg.Access == "" {
		p.Cfg.Access = AccessRead
	}
	if p.Cfg.CreatedAt.IsZero() {
		p.Cfg.CreatedAt = time.Now().UTC()
	}
	if err := p.Save(); err != nil {
		return nil, runtimeErr(err)
	}
	return res, nil
}

// Logout terminates the session on Telegram's side (unless local is set) and
// deletes the local files.
func (p *Profile) Logout(ctx context.Context, o Options, local bool) (revoked bool, err error) {
	o.defaults()
	if !local && p.Exists() && p.Cfg.APIID != 0 {
		err = p.Open(ctx, o, func(ctx context.Context, c *Conn) error {
			_, e := c.API.AuthLogOut(ctx)
			return wrapRPCOrNil(e)
		})
		if err != nil {
			return false, err
		}
		revoked = true
	}
	for _, f := range []string{p.SessionPath(), p.SessionPath() + ".bak", p.PeersPath(), p.ConfigPath()} {
		if e := os.Remove(f); e != nil && !os.IsNotExist(e) {
			return revoked, runtimeErr(e)
		}
	}
	return revoked, nil
}

// StatusInfo describes the profile and the health of its session.
func (c *Conn) Status(ctx context.Context) (map[string]any, error) {
	out := map[string]any{
		"profile": c.Profile.Name,
		"user":    Who{ID: c.Self.ID, Type: "user", Name: userName(c.Self), Username: c.Self.Username},
		"access":  c.Profile.Cfg.Access,
		"layer":   Layer(),
	}
	res, err := c.API.AccountGetAuthorizations(ctx)
	if err != nil {
		return out, nil
	}
	ttl := res.AuthorizationTTLDays
	out["session_ttl_days"] = ttl
	for _, a := range res.Authorizations {
		if !a.Current {
			continue
		}
		out["session_created"] = rfc3339(a.DateCreated)
		out["session_last_active"] = rfc3339(a.DateActive)
		if ttl > 0 {
			out["session_expires_if_idle"] = time.Unix(int64(a.DateActive), 0).UTC().AddDate(0, 0, ttl).Format(time.RFC3339)
		}
	}
	out["other_sessions"] = len(res.Authorizations) - 1
	return out, nil
}

const bundlePrefix = "tgbot1:"

type sessionBundle struct {
	APIID   int    `json:"api_id"`
	Session []byte `json:"session"`
}

// ExportSession packs the stored session so it can be moved to another machine.
// Whoever holds the string has full access to the account.
func (p *Profile) ExportSession() (string, error) {
	data, err := (&fileStorage{path: p.SessionPath()}).LoadSession(context.Background())
	if err != nil {
		return "", &Error{Kind: KindAuth, Message: fmt.Sprintf("no session for profile %q: log in first", p.Name)}
	}
	b, _ := json.Marshal(sessionBundle{APIID: p.Cfg.APIID, Session: data})
	return bundlePrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// ParseSessionBundle unpacks the output of ExportSession.
func ParseSessionBundle(s string) (apiID int, data []byte, err error) {
	s = strings.TrimSpace(s)
	raw, derr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, bundlePrefix))
	var b sessionBundle
	if !strings.HasPrefix(s, bundlePrefix) || derr != nil || json.Unmarshal(raw, &b) != nil || len(b.Session) == 0 {
		return 0, nil, usagef("not a tgbot session string (it starts with %q and comes from `tgbot user session export`)", bundlePrefix)
	}
	return b.APIID, b.Session, nil
}
