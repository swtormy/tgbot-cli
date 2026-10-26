package mtp

import (
	"context"
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
	Code            string // non-interactive step 2
	Password        string // 2FA password, only for this call
	Prompt          func(label string, secret bool) (string, error)
	ShowQR          func(url string)
}

type LoginResult struct {
	AlreadyLoggedIn bool
	CodeSent        bool
	CodeVia         string
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
}

func (a *prompter) Phone(ctx context.Context) (string, error) {
	if a.phone != "" {
		return a.phone, nil
	}
	return a.o.Prompt("Phone number (with country code, e.g. +15551234567)", false)
}

func (a *prompter) Code(ctx context.Context, sent *tg.AuthSentCode) (string, error) {
	where := strings.TrimPrefix(fmt.Sprintf("%T", sent.Type), "*tg.AuthSentCodeType")
	where = strings.TrimPrefix(where, "*tg.AuthSentCodeTypeApp")
	return a.o.Prompt(fmt.Sprintf("Login code (sent via %s; the Telegram app is the usual place)", strings.ToLower(where)), false)
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

// Login signs in and stores the session. If the profile already has a working
// session it does nothing: a new login is never needed while the key is valid.
func (p *Profile) Login(ctx context.Context, o LoginOptions) (*LoginResult, error) {
	o.defaults()
	if o.APIID == 0 || o.APIHash == "" {
		return nil, usagef("api_id and api_hash are required (get them at https://my.telegram.org > API development tools)")
	}
	if o.Prompt == nil && o.Phone == "" && o.TelethonSession == "" {
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
			res.AlreadyLoggedIn, res.Self = o.TelethonSession == "", st.User
			return nil
		}
		if o.TelethonSession != "" {
			return &Error{Kind: KindAuth, Message: "Telegram does not accept this Telethon session (it was logged out or revoked)"}
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
				pw, perr := o.Prompt("Two-step verification password (not stored anywhere)", true)
				if perr != nil {
					return perr
				}
				_, err = client.Auth().Password(ctx, pw)
			}
			if err != nil {
				return wrapRPC(err)
			}
		} else if o.Prompt != nil {
			flow := auth.NewFlow(&prompter{o: o, phone: o.Phone}, auth.SendCodeOptions{})
			if err := client.Auth().IfNecessary(ctx, flow); err != nil {
				return wrapRPC(err)
			}
		} else if o.Code == "" {
			sent, err := client.Auth().SendCode(ctx, o.Phone, auth.SendCodeOptions{})
			if err != nil {
				return wrapRPC(err)
			}
			s, ok := sent.(*tg.AuthSentCode)
			if !ok {
				return &Error{Kind: KindAuth, Message: "Telegram did not send a code (unexpected response)"}
			}
			pb, _ := json.Marshal(pendingLogin{Phone: o.Phone, Hash: s.PhoneCodeHash, At: time.Now().UTC()})
			if err := writeAtomic(p.pendingPath(), pb); err != nil {
				return runtimeErr(err)
			}
			res.CodeSent = true
			res.CodeVia = strings.ToLower(strings.TrimPrefix(fmt.Sprintf("%T", s.Type), "*tg.AuthSentCodeType"))
			return nil
		} else {
			pb, err := os.ReadFile(p.pendingPath())
			var pend pendingLogin
			if err != nil || json.Unmarshal(pb, &pend) != nil || pend.Phone != o.Phone {
				return usagef("no code was requested for %s: run `tgbot user login --phone %s` first", o.Phone, o.Phone)
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
