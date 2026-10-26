package mtp

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// PeerEntry is what the cache remembers about a user, group or channel.
type PeerEntry struct {
	Type       string `json:"type"` // user, chat, channel
	ID         int64  `json:"id"`
	AccessHash int64  `json:"access_hash,omitempty"`
	Title      string `json:"title,omitempty"`
	Username   string `json:"username,omitempty"`
	Phone      string `json:"phone,omitempty"`
}

// Peers caches access hashes. Telegram only accepts a numeric user or channel
// id together with the access hash it handed out earlier, so ids from a
// previous run are resolvable only if we kept them.
type Peers struct {
	path  string
	mu    sync.Mutex
	m     map[string]PeerEntry
	dirty bool
}

func peerKey(typ string, id int64) string { return typ + ":" + strconv.FormatInt(id, 10) }

func LoadPeers(path string) *Peers {
	p := &Peers{path: path, m: map[string]PeerEntry{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &p.m)
	}
	return p
}

func (p *Peers) Save() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dirty || p.path == "" {
		return
	}
	if b, err := json.Marshal(p.m); err == nil {
		_ = writeAtomic(p.path, b)
	}
}

func (p *Peers) put(e PeerEntry) {
	k := peerKey(e.Type, e.ID)
	old, ok := p.m[k]
	if ok && e.AccessHash == 0 {
		e.AccessHash = old.AccessHash
	}
	if ok && old == e {
		return
	}
	p.m[k] = e
	p.dirty = true
}

// Apply remembers every user and chat from an API response.
func (p *Peers) Apply(users []tg.UserClass, chats []tg.ChatClass) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, uc := range users {
		if u, ok := uc.(*tg.User); ok && !u.Min {
			p.put(PeerEntry{Type: "user", ID: u.ID, AccessHash: u.AccessHash, Title: userName(u), Username: u.Username, Phone: u.Phone})
		}
	}
	for _, cc := range chats {
		switch c := cc.(type) {
		case *tg.Chat:
			p.put(PeerEntry{Type: "chat", ID: c.ID, Title: c.Title})
		case *tg.Channel:
			if !c.Min {
				p.put(PeerEntry{Type: "channel", ID: c.ID, AccessHash: c.AccessHash, Title: c.Title, Username: c.Username})
			}
		case *tg.ChannelForbidden:
			p.put(PeerEntry{Type: "channel", ID: c.ID, AccessHash: c.AccessHash, Title: c.Title})
		}
	}
}

func (p *Peers) get(typ string, id int64) (PeerEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.m[peerKey(typ, id)]
	return e, ok
}

func (p *Peers) byUsername(name string) (PeerEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.m {
		if strings.EqualFold(e.Username, name) {
			return e, true
		}
	}
	return PeerEntry{}, false
}

func (p *Peers) byPhone(phone string) (PeerEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.m {
		if e.Phone != "" && e.Phone == phone {
			return e, true
		}
	}
	return PeerEntry{}, false
}

// byTitle matches a chat or person by name: exact first, then substring.
func (p *Peers) byTitle(q string) []PeerEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	q = strings.ToLower(q)
	var exact, part []PeerEntry
	for _, e := range p.m {
		t := strings.ToLower(e.Title)
		switch {
		case t == q:
			exact = append(exact, e)
		case t != "" && strings.Contains(t, q):
			part = append(part, e)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return part
}

// Peer ids follow the Bot API / TDLib convention so ids look the same in every
// tool: users are positive, basic groups are -id, channels and supergroups are
// -100<id>.
const channelBase = 1_000_000_000_000

func PublicID(typ string, id int64) int64 {
	switch typ {
	case "chat":
		return -id
	case "channel":
		return -channelBase - id
	}
	return id
}

func splitPublicID(v int64) (typ string, id int64) {
	switch {
	case v >= 0:
		return "user", v
	case v < -channelBase:
		return "channel", -v - channelBase
	}
	return "chat", -v
}

func (e PeerEntry) Input() tg.InputPeerClass {
	switch e.Type {
	case "chat":
		return &tg.InputPeerChat{ChatID: e.ID}
	case "channel":
		return &tg.InputPeerChannel{ChannelID: e.ID, AccessHash: e.AccessHash}
	}
	return &tg.InputPeerUser{UserID: e.ID, AccessHash: e.AccessHash}
}

func (e PeerEntry) InputUser() tg.InputUserClass {
	return &tg.InputUser{UserID: e.ID, AccessHash: e.AccessHash}
}

func (e PeerEntry) InputChannel() tg.InputChannelClass {
	return &tg.InputChannel{ChannelID: e.ID, AccessHash: e.AccessHash}
}

func userName(u *tg.User) string {
	n := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if n == "" {
		n = u.Username
	}
	if n == "" && u.Deleted {
		n = "Deleted Account"
	}
	return n
}

var (
	usernameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{2,31}$`)
	linkRe     = regexp.MustCompile(`^(?:https?://)?(?:t|telegram)\.me/(.+)$`)
	numRe      = regexp.MustCompile(`^-?[0-9]+$`)
)

// Resolve turns a user-supplied reference into an entry.
//
// Accepted: me, @username, username, t.me/username, t.me/c/<channel>/<msg>,
// a numeric id as printed by this tool, +phone, or a chat title.
func (c *Conn) Resolve(ctx context.Context, ref string) (PeerEntry, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return PeerEntry{}, usagef("empty peer")
	}
	switch strings.ToLower(ref) {
	case "me", "self", "saved":
		return PeerEntry{Type: "user", ID: c.Self.ID, AccessHash: c.Self.AccessHash, Title: userName(c.Self), Username: c.Self.Username}, nil
	}
	if m := linkRe.FindStringSubmatch(ref); m != nil {
		path := strings.TrimSuffix(strings.SplitN(m[1], "?", 2)[0], "/")
		parts := strings.Split(path, "/")
		switch {
		case strings.HasPrefix(parts[0], "+") || parts[0] == "joinchat":
			return PeerEntry{}, usagef("%q is an invite link: use `tgbot user join` to join, then refer to the chat by id or name", ref)
		case parts[0] == "c" && len(parts) >= 2:
			id, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return PeerEntry{}, usagef("bad link %q", ref)
			}
			return c.byID(ctx, PublicID("channel", id))
		}
		ref = parts[0]
	}
	switch {
	case strings.HasPrefix(ref, "@"):
		return c.byUsername(ctx, strings.TrimPrefix(ref, "@"))
	case strings.HasPrefix(ref, "+"):
		return c.byPhone(ctx, strings.TrimPrefix(ref, "+"))
	case numRe.MatchString(ref):
		id, err := strconv.ParseInt(ref, 10, 64)
		if err != nil {
			return PeerEntry{}, usagef("bad id %q", ref)
		}
		return c.byID(ctx, id)
	}
	if hits := c.Peers.byTitle(ref); len(hits) == 1 {
		return hits[0], nil
	} else if len(hits) > 1 {
		return PeerEntry{}, ambiguous(ref, hits)
	}
	if usernameRe.MatchString(ref) {
		return c.byUsername(ctx, ref)
	}
	// The title cache only knows chats seen before: load recent dialogs once and retry.
	if err := c.refreshDialogs(ctx, 300); err != nil {
		return PeerEntry{}, err
	}
	hits := c.Peers.byTitle(ref)
	switch len(hits) {
	case 0:
		return PeerEntry{}, usagef("no chat or person matches %q (use @username, a numeric id from `tgbot user dialogs`, or +phone)", ref)
	case 1:
		return hits[0], nil
	}
	return PeerEntry{}, ambiguous(ref, hits)
}

func ambiguous(ref string, hits []PeerEntry) error {
	var b strings.Builder
	for i, h := range hits {
		if i == 5 {
			b.WriteString(", ...")
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.FormatInt(PublicID(h.Type, h.ID), 10) + " (" + h.Title + ")")
	}
	return usagef("%q matches several chats: %s. Use the numeric id.", ref, b.String())
}

func (c *Conn) byID(ctx context.Context, public int64) (PeerEntry, error) {
	typ, id := splitPublicID(public)
	if e, ok := c.Peers.get(typ, id); ok {
		return e, nil
	}
	if typ == "chat" {
		return PeerEntry{Type: "chat", ID: id}, nil
	}
	if typ == "user" && id == c.Self.ID {
		return c.Resolve(ctx, "me")
	}
	if err := c.refreshDialogs(ctx, 500); err != nil {
		return PeerEntry{}, err
	}
	if e, ok := c.Peers.get(typ, id); ok {
		return e, nil
	}
	return PeerEntry{}, usagef("id %d is not known to this account. Telegram needs an access hash for it: resolve it once with @username or +phone, or find the chat in `tgbot user dialogs`", public)
}

func (c *Conn) byUsername(ctx context.Context, name string) (PeerEntry, error) {
	if e, ok := c.Peers.byUsername(name); ok {
		return e, nil
	}
	res, err := c.API.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: name})
	if err != nil {
		if rpcIs(err, "USERNAME_NOT_OCCUPIED", "USERNAME_INVALID") {
			return PeerEntry{}, usagef("no user, group or channel with username @%s", name)
		}
		return PeerEntry{}, wrapRPC(err)
	}
	c.Peers.Apply(res.Users, res.Chats)
	return c.fromPeer(res.Peer)
}

func (c *Conn) byPhone(ctx context.Context, phone string) (PeerEntry, error) {
	if e, ok := c.Peers.byPhone(phone); ok {
		return e, nil
	}
	res, err := c.API.ContactsResolvePhone(ctx, phone)
	if err != nil {
		if rpcIs(err, "PHONE_NOT_OCCUPIED") {
			return PeerEntry{}, usagef("no Telegram account for +%s (or its privacy settings hide it)", phone)
		}
		return PeerEntry{}, wrapRPC(err)
	}
	c.Peers.Apply(res.Users, res.Chats)
	return c.fromPeer(res.Peer)
}

func (c *Conn) fromPeer(p tg.PeerClass) (PeerEntry, error) {
	var typ string
	var id int64
	switch t := p.(type) {
	case *tg.PeerUser:
		typ, id = "user", t.UserID
	case *tg.PeerChat:
		typ, id = "chat", t.ChatID
	case *tg.PeerChannel:
		typ, id = "channel", t.ChannelID
	}
	if e, ok := c.Peers.get(typ, id); ok {
		return e, nil
	}
	return PeerEntry{}, &Error{Kind: KindRuntime, Message: "Telegram did not return access details for the peer"}
}

// refreshDialogs loads recent dialogs into the cache.
func (c *Conn) refreshDialogs(ctx context.Context, max int) error {
	n := 0
	return c.walkDialogs(ctx, 0, func(Dialog) bool { n++; return n < max })
}

func rpcIs(err error, types ...string) bool { return tgerr.Is(err, types...) }
