package mtp

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/gofrs/flock"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Options tune one connected run.
type Options struct {
	Version      string
	Stderr       io.Writer
	MaxFloodWait time.Duration // longest FLOOD_WAIT that is waited out instead of reported
	LockTimeout  time.Duration // how long to queue behind another tgbot process
}

// Conn is a connected, authorized account.
type Conn struct {
	API     *tg.Client
	Self    *tg.User
	Profile *Profile
	Peers   *Peers
	Client  *telegram.Client
}

func (o *Options) defaults() {
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.MaxFloodWait == 0 {
		o.MaxFloodWait = 30 * time.Second
	}
	if o.LockTimeout == 0 {
		o.LockTimeout = 90 * time.Second
	}
	if o.Version == "" {
		o.Version = "dev"
	}
}

// lock serialises every process using this profile. Telegram kills a session
// that is used from two connections at once (AUTH_KEY_DUPLICATED), so parallel
// agent calls must queue rather than race.
func (p *Profile) lock(ctx context.Context, timeout time.Duration) (func(), error) {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return nil, runtimeErr(err)
	}
	fl := flock.New(p.LockPath())
	lctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ok, err := fl.TryLockContext(lctx, 200*time.Millisecond)
	if err != nil || !ok {
		return nil, &Error{Kind: KindRuntime, Message: fmt.Sprintf(
			"another tgbot user command has held profile %q for %s; wait for it to finish", p.Name, timeout)}
	}
	return func() { _ = fl.Unlock() }, nil
}

func (p *Profile) newClient(o Options, handler telegram.UpdateHandler) *telegram.Client {
	opts := telegram.Options{
		SessionStorage: &fileStorage{path: p.SessionPath()},
		Device: telegram.DeviceConfig{
			DeviceModel:    "tgbot-cli",
			SystemVersion:  runtime.GOOS,
			AppVersion:     o.Version,
			SystemLangCode: "en",
			LangCode:       "en",
		},
		Middlewares: []telegram.Middleware{floodWaiter(o.MaxFloodWait, o.Stderr)},
		DialTimeout: 20 * time.Second,
	}
	if handler != nil {
		opts.UpdateHandler = handler
	}
	return telegram.NewClient(p.Cfg.APIID, p.Cfg.APIHash, opts)
}

func floodWaiter(max time.Duration, log io.Writer) telegram.Middleware {
	return telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
			for attempt := 0; ; attempt++ {
				err := next.Invoke(ctx, in, out)
				d, ok := tgerr.AsFloodWait(err)
				if !ok || d > max || attempt >= 3 {
					return err
				}
				fmt.Fprintf(log, "telegram asked to wait %s (FLOOD_WAIT); waiting\n", d.Round(time.Second))
				select {
				case <-time.After(d + time.Second):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	})
}

// Open connects with the stored session and runs fn. It fails with KindAuth
// when the session is missing or no longer valid.
func (p *Profile) Open(ctx context.Context, o Options, fn func(ctx context.Context, c *Conn) error) error {
	return p.openWith(ctx, o, nil, fn)
}

func (p *Profile) openWith(ctx context.Context, o Options, handler telegram.UpdateHandler, fn func(ctx context.Context, c *Conn) error) error {
	o.defaults()
	if p.Cfg.APIID == 0 || p.Cfg.APIHash == "" {
		return &Error{Kind: KindAuth, Message: "api_id/api_hash are missing: run `tgbot user login`"}
	}
	if !p.Exists() {
		return &Error{Kind: KindAuth, Message: fmt.Sprintf("no session for profile %q: run `tgbot user login` in a terminal", p.Name)}
	}
	unlock, err := p.lock(ctx, o.LockTimeout)
	if err != nil {
		return err
	}
	defer unlock()

	peers := LoadPeers(p.PeersPath())
	client := p.newClient(o, handler)
	var inner error
	err = client.Run(ctx, func(ctx context.Context) error {
		st, err := client.Auth().Status(ctx)
		if err != nil {
			return wrapRPC(err)
		}
		if !st.Authorized {
			return &Error{Kind: KindAuth, Message: "the stored session is not authorized any more. Run `tgbot user login` again."}
		}
		inner = fn(ctx, &Conn{API: client.API(), Self: st.User, Profile: p, Peers: peers, Client: client})
		return nil
	})
	peers.Save()
	if inner != nil {
		return inner
	}
	if err != nil {
		return runtimeErr(err)
	}
	return nil
}
