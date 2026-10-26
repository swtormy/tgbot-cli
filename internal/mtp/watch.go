package mtp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gotd/td/tg"
)

func (p *Profile) cursorsPath() string { return filepath.Join(p.Dir, "cursors.json") }

type PollOpts struct {
	Peers      []string // limit to these chats
	PerChat    int
	Scan       int
	Peek       bool // do not advance the cursors
	IncludeOwn bool
}

type PollResult struct {
	Baseline  bool        `json:"baseline,omitempty"` // first run: cursors were just set, nothing is returned
	Chats     []InboxChat `json:"chats"`
	Untrusted bool        `json:"untrusted"`
}

// Poll returns messages that arrived since the previous poll. Cursors (the last
// seen message id per chat) live in the profile directory, so no daemon is
// needed: call it from a cron job or an agent loop.
func (c *Conn) Poll(ctx context.Context, o PollOpts) (*PollResult, error) {
	if o.PerChat <= 0 {
		o.PerChat = 50
	}
	if o.Scan <= 0 {
		o.Scan = 60
	}
	cursors := map[string]int{}
	if b, err := os.ReadFile(c.Profile.cursorsPath()); err == nil {
		_ = json.Unmarshal(b, &cursors)
	}
	baseline := len(cursors) == 0
	only := map[int64]bool{}
	for _, ref := range o.Peers {
		pe, err := c.Resolve(ctx, ref)
		if err != nil {
			return nil, err
		}
		only[PublicID(pe.Type, pe.ID)] = true
	}
	res := &PollResult{Baseline: baseline, Chats: []InboxChat{}, Untrusted: true}
	var dialogs []Dialog
	n := 0
	err := c.walkDialogs(ctx, 0, func(d Dialog) bool {
		dialogs = append(dialogs, d)
		n++
		return n < o.Scan
	})
	if err != nil {
		return nil, err
	}
	next := map[string]int{}
	for k, v := range cursors {
		next[k] = v
	}
	for _, d := range dialogs {
		if len(only) > 0 && !only[d.ID] {
			continue
		}
		if d.LastMessage == nil || d.peer.ID == 0 {
			continue
		}
		key := strconv.FormatInt(d.ID, 10)
		top := d.LastMessage.ID
		cur, known := cursors[key]
		next[key] = top
		if baseline || !known || top <= cur {
			continue
		}
		msgs, _, err := c.fetchHistory(ctx, d.peer, HistoryOpts{Limit: o.PerChat, MinID: cur})
		if err != nil {
			return nil, err
		}
		if !o.IncludeOwn {
			kept := msgs[:0]
			for _, m := range msgs {
				if !m.Out {
					kept = append(kept, m)
				}
			}
			msgs = kept
		}
		if len(msgs) == 0 {
			continue
		}
		ic := InboxChat{Chat: d.peer.Chat(), Unread: d.Unread, Messages: msgs}
		if d.Title != "" {
			ic.Chat.Name = d.Title
		}
		res.Chats = append(res.Chats, ic)
	}
	if !o.Peek {
		b, _ := json.Marshal(next)
		if err := writeAtomic(c.Profile.cursorsPath(), b); err != nil {
			return nil, runtimeErr(err)
		}
	}
	return res, nil
}

type WatchOpts struct {
	Peers      []string
	For        time.Duration // 0 = until interrupted
	Max        int           // stop after this many messages; 0 = no limit
	IncludeOwn bool
}

// Watch streams new messages live over one connection until the window ends.
func (p *Profile) Watch(ctx context.Context, o Options, w WatchOpts, emit func(Message) error) error {
	d := tg.NewUpdateDispatcher()
	var (
		mu    sync.Mutex
		only  = map[int64]bool{}
		count int
		self  int64
		fail  error
	)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var peers *Peers
	handle := func(e tg.Entities, mc tg.MessageClass) error {
		mu.Lock()
		defer mu.Unlock()
		var users []tg.UserClass
		for _, u := range e.Users {
			users = append(users, u)
		}
		var chats []tg.ChatClass
		for _, c := range e.Chats {
			chats = append(chats, c)
		}
		for _, c := range e.Channels {
			chats = append(chats, c)
		}
		peers.Apply(users, chats)
		en := newEnts(self, users, chats)
		m, ok := en.message(mc, true)
		if !ok {
			return nil
		}
		if len(only) > 0 && !only[peerPublicID(messagePeer(mc))] {
			return nil
		}
		if m.Out && !w.IncludeOwn {
			return nil
		}
		if err := emit(m); err != nil {
			fail = err
			cancel()
			return nil
		}
		count++
		if w.Max > 0 && count >= w.Max {
			cancel()
		}
		return nil
	}
	d.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error { return handle(e, u.Message) })
	d.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		return handle(e, u.Message)
	})
	err := p.openWith(cctx, o, d, func(ctx context.Context, c *Conn) error {
		mu.Lock()
		self, peers = c.Self.ID, c.Peers
		for _, ref := range w.Peers {
			pe, err := c.Resolve(ctx, ref)
			if err != nil {
				mu.Unlock()
				return err
			}
			only[PublicID(pe.Type, pe.ID)] = true
		}
		mu.Unlock()
		if _, err := c.API.UpdatesGetState(ctx); err != nil {
			return wrapRPC(err)
		}
		var timer <-chan time.Time
		if w.For > 0 {
			timer = time.After(w.For)
		}
		select {
		case <-ctx.Done():
		case <-timer:
		}
		return nil
	})
	if fail != nil {
		return fail
	}
	if err != nil && cctx.Err() != nil && ctx.Err() == nil {
		return nil
	}
	return err
}
