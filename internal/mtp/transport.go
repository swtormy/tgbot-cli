package mtp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"golang.org/x/net/proxy"
)

const (
	TransportAuto      = "auto"
	TransportTCP       = "tcp"
	TransportWebsocket = "websocket"
)

var errConnectTimeout = errors.New("no answer from Telegram")

// resolverFor builds the way to reach Telegram's data centers. "tcp" is the
// native protocol on port 443 and others; "websocket" is the same protocol
// wrapped in HTTPS to *.web.telegram.org, which passes firewalls that only
// allow web traffic.
func resolverFor(transport, proxyURL string) (dcs.Resolver, error) {
	var u *url.URL
	if proxyURL != "" {
		var err error
		if u, err = url.Parse(proxyURL); err != nil || u.Host == "" {
			return nil, usagef("bad proxy %q: use socks5://host:port or http://host:port", proxyURL)
		}
		switch u.Scheme {
		case "socks5", "socks5h", "http", "https":
		default:
			return nil, usagef("bad proxy scheme %q: use socks5, http or https", u.Scheme)
		}
	}
	if transport == TransportWebsocket {
		opts := dcs.WebsocketOptions{}
		if u != nil {
			opts.DialOptions = &websocket.DialOptions{
				Subprotocols: []string{"binary"},
				HTTPClient:   &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}},
			}
		}
		return dcs.Websocket(opts), nil
	}
	if u == nil {
		return dcs.DefaultResolver(), nil
	}
	if !strings.HasPrefix(u.Scheme, "socks5") {
		return nil, usagef("the tcp transport needs a socks5:// proxy; an http proxy works with --transport websocket")
	}
	d, err := proxy.FromURL(u, proxy.Direct)
	if err != nil {
		return nil, usagef("bad proxy %q: %v", proxyURL, err)
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, usagef("proxy %q cannot dial with a context", proxyURL)
	}
	return dcs.Plain(dcs.PlainOptions{Dial: cd.DialContext}), nil
}

// transportOrder lists what to try, in order. Automatic mode tries the native
// protocol first and falls back to WebSocket, then remembers a working
// WebSocket so later runs do not wait for the failing attempt.
func (p *Profile) transportOrder(o Options) ([]string, error) {
	want := o.Transport
	if want == "" || want == TransportAuto {
		want = p.Cfg.Transport
	}
	httpProxy := strings.HasPrefix(o.Proxy, "http")
	switch want {
	case TransportTCP:
		if httpProxy {
			return nil, usagef("the tcp transport needs a socks5:// proxy; an http proxy works with --transport websocket")
		}
		return []string{TransportTCP}, nil
	case TransportWebsocket:
		if o.Transport == TransportWebsocket {
			return []string{TransportWebsocket}, nil
		}
		return []string{TransportWebsocket, TransportTCP}, nil
	case "", TransportAuto:
		if httpProxy {
			return []string{TransportWebsocket}, nil
		}
		return []string{TransportTCP, TransportWebsocket}, nil
	}
	return nil, usagef("--transport must be auto, tcp or websocket")
}

// runClient connects (trying the transports in order) and runs fn once the
// connection is up. It never hangs silently: if a transport gets no answer in
// o.ConnectTimeout it says so and moves on, and it ends with a clear error.
func (p *Profile) runClient(ctx context.Context, o Options, handler telegram.UpdateHandler, fn func(ctx context.Context, client *telegram.Client) error) error {
	order, err := p.transportOrder(o)
	if err != nil {
		return err
	}
	var tried []string
	for i, tr := range order {
		res, err := resolverFor(tr, o.Proxy)
		if err != nil {
			return err
		}
		client := p.newClient(o, handler, res)
		cctx, cancel := context.WithCancelCause(ctx)
		reached := false
		deadline := time.AfterFunc(o.ConnectTimeout, func() { cancel(errConnectTimeout) })
		slow := time.AfterFunc(5*time.Second, func() {
			fmt.Fprintf(o.Stderr, "still connecting to Telegram (%s)...\n", tr)
		})
		runErr := client.Run(cctx, func(rctx context.Context) error {
			reached = true
			deadline.Stop()
			slow.Stop()
			return fn(rctx, client)
		})
		deadline.Stop()
		slow.Stop()
		timedOut := !reached && errors.Is(context.Cause(cctx), errConnectTimeout)
		cancel(nil)
		if reached {
			if tr == TransportWebsocket && i > 0 && p.Cfg.Transport != TransportWebsocket {
				p.Cfg.Transport = TransportWebsocket
				if p.Cfg.APIID != 0 {
					_ = p.Save()
				}
			}
			return runErr
		}
		if !timedOut {
			return runErr
		}
		tried = append(tried, fmt.Sprintf("%s (no answer in %s)", tr, o.ConnectTimeout))
		if i+1 < len(order) {
			fmt.Fprintf(o.Stderr, "%s connection to Telegram got no answer in %s; trying %s\n", tr, o.ConnectTimeout, order[i+1])
		}
	}
	return &Error{Kind: KindRuntime, Message: "cannot reach Telegram: tried " + strings.Join(tried, ", ") +
		". The network is blocking or filtering Telegram's protocol. Use a proxy (--proxy socks5://host:port or http://host:port, or TGBOT_PROXY), " +
		"or run from another network. Bot-mode commands only need api.telegram.org over HTTPS and are not affected."}
}
