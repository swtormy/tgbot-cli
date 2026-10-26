package mtp

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/markdown"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

func findMessage(msgs []tg.MessageClass, peer tg.PeerClass, id int) tg.MessageClass {
	want := peerPublicID(peer)
	for _, m := range msgs {
		if m.GetID() == id && peerPublicID(messagePeer(m)) == want {
			return m
		}
	}
	return nil
}

func (c *Conn) dialogView(e *ents, d *tg.Dialog, msgs []tg.MessageClass) Dialog {
	w := e.who(d.Peer)
	if w == nil {
		w = &Who{}
	}
	out := Dialog{
		ID: peerPublicID(d.Peer), Type: w.Type, Title: w.Name, Username: w.Username,
		Unread: d.UnreadCount, UnreadMentions: d.UnreadMentionsCount, Pinned: d.Pinned,
		readInboxMaxID: d.ReadInboxMaxID,
	}
	if pu, ok := d.Peer.(*tg.PeerUser); ok && pu.UserID == c.Self.ID {
		out.Title, out.Type = "Saved Messages", "saved"
	}
	if f, ok := d.GetFolderID(); ok && f == 1 {
		out.Archived = true
	}
	if until, ok := d.NotifySettings.GetMuteUntil(); ok && int64(until) > time.Now().Unix() {
		out.Muted = true
	}
	if top := findMessage(msgs, d.Peer, d.TopMessage); top != nil {
		if m, ok := e.message(top, false); ok {
			out.LastMessage = &m
		}
	}
	typ, id := splitPublicID(out.ID)
	out.peer, _ = c.Peers.get(typ, id)
	return out
}

// walkDialogs streams the chat list, newest first, until fn returns false.
func (c *Conn) walkDialogs(ctx context.Context, folder int, fn func(Dialog) bool) error {
	var offDate, offID int
	var offPeer tg.InputPeerClass = &tg.InputPeerEmpty{}
	for {
		req := &tg.MessagesGetDialogsRequest{OffsetDate: offDate, OffsetID: offID, OffsetPeer: offPeer, Limit: 100}
		if folder > 0 {
			req.SetFolderID(folder)
		}
		res, err := c.API.MessagesGetDialogs(ctx, req)
		if err != nil {
			return wrapRPC(err)
		}
		d, ok := res.AsModified()
		if !ok {
			return nil
		}
		c.Peers.Apply(d.GetUsers(), d.GetChats())
		e := newEnts(c.Self.ID, d.GetUsers(), d.GetChats())
		msgs := d.GetMessages()
		raw := d.GetDialogs()
		var last *tg.Dialog
		for _, dc := range raw {
			dd, ok := dc.(*tg.Dialog)
			if !ok {
				continue
			}
			last = dd
			if !fn(c.dialogView(e, dd, msgs)) {
				return nil
			}
		}
		if len(raw) < 100 || last == nil {
			return nil
		}
		top := findMessage(msgs, last.Peer, last.TopMessage)
		if top == nil {
			return nil
		}
		pe, err := c.fromPeer(last.Peer)
		if err != nil {
			return nil
		}
		offID, offDate, offPeer = last.TopMessage, messageTime(top), pe.Input()
	}
}

type DialogsOpts struct {
	Limit    int
	Unread   bool
	Archived bool
	Query    string
}

func (c *Conn) Dialogs(ctx context.Context, o DialogsOpts) ([]Dialog, error) {
	if o.Limit <= 0 {
		o.Limit = 30
	}
	folder := 0
	if o.Archived {
		folder = 1
	}
	q := strings.ToLower(o.Query)
	out := []Dialog{}
	err := c.walkDialogs(ctx, folder, func(d Dialog) bool {
		if o.Unread && d.Unread == 0 && d.UnreadMentions == 0 {
			return true
		}
		if q != "" && !strings.Contains(strings.ToLower(d.Title), q) && !strings.Contains(strings.ToLower(d.Username), q) {
			return true
		}
		out = append(out, d)
		return len(out) < o.Limit
	})
	return out, err
}

// Chat identifies a chat in results.
func (e PeerEntry) Chat() Who {
	t := e.Type
	if t == "chat" {
		t = "group"
	}
	return Who{ID: PublicID(e.Type, e.ID), Type: t, Name: e.Title, Username: e.Username}
}

type HistoryOpts struct {
	Limit    int
	OffsetID int // only messages older than this id
	MinID    int // only messages newer than this id
	Since    time.Time
	Until    time.Time
}

type History struct {
	Chat      Who       `json:"chat"`
	Messages  []Message `json:"messages"`
	OlderThan int       `json:"older_than,omitempty"` // pass as --before to continue backwards
	Untrusted bool      `json:"untrusted"`
}

func (c *Conn) History(ctx context.Context, p PeerEntry, o HistoryOpts) (*History, error) {
	msgs, more, err := c.fetchHistory(ctx, p, o)
	if err != nil {
		return nil, err
	}
	h := &History{Chat: p.Chat(), Messages: msgs, Untrusted: true}
	if more && len(msgs) > 0 {
		h.OlderThan = msgs[0].ID
	}
	return h, nil
}

// fetchHistory returns messages oldest-first. more reports whether older ones exist.
func (c *Conn) fetchHistory(ctx context.Context, p PeerEntry, o HistoryOpts) ([]Message, bool, error) {
	if o.Limit <= 0 {
		o.Limit = 20
	}
	if o.Limit > 1000 {
		o.Limit = 1000
	}
	offID := o.OffsetID
	var newestFirst []Message
	more := false
	for len(newestFirst) < o.Limit {
		want := o.Limit - len(newestFirst)
		if want > 100 {
			want = 100
		}
		req := &tg.MessagesGetHistoryRequest{Peer: p.Input(), OffsetID: offID, Limit: want, MinID: o.MinID}
		if !o.Until.IsZero() {
			req.OffsetDate = int(o.Until.Unix())
		}
		res, err := c.API.MessagesGetHistory(ctx, req)
		if err != nil {
			return nil, false, wrapRPC(err)
		}
		m, ok := res.AsModified()
		if !ok {
			break
		}
		c.Peers.Apply(m.GetUsers(), m.GetChats())
		e := newEnts(c.Self.ID, m.GetUsers(), m.GetChats())
		raw := m.GetMessages()
		stop := false
		for _, mc := range raw {
			v, ok := e.message(mc, false)
			if !ok {
				continue
			}
			if !o.Since.IsZero() && messageTime(mc) < int(o.Since.Unix()) {
				stop = true
				break
			}
			newestFirst = append(newestFirst, v)
		}
		if len(raw) > 0 {
			offID = raw[len(raw)-1].GetID()
		}
		more = len(raw) >= want
		if stop || len(raw) < want {
			more = more && !stop
			break
		}
	}
	for i, j := 0, len(newestFirst)-1; i < j; i, j = i+1, j-1 {
		newestFirst[i], newestFirst[j] = newestFirst[j], newestFirst[i]
	}
	return newestFirst, more, nil
}

type SearchOpts struct {
	Query  string
	Peer   *PeerEntry
	From   *PeerEntry
	Filter string
	Limit  int
	Since  time.Time
	Until  time.Time
	Before int
}

type SearchResult struct {
	Messages  []Message `json:"messages"`
	Total     int       `json:"total,omitempty"`
	Untrusted bool      `json:"untrusted"`
}

var searchFilters = map[string]tg.MessagesFilterClass{
	"photo":      &tg.InputMessagesFilterPhotos{},
	"video":      &tg.InputMessagesFilterVideo{},
	"photovideo": &tg.InputMessagesFilterPhotoVideo{},
	"document":   &tg.InputMessagesFilterDocument{},
	"url":        &tg.InputMessagesFilterURL{},
	"gif":        &tg.InputMessagesFilterGif{},
	"voice":      &tg.InputMessagesFilterVoice{},
	"music":      &tg.InputMessagesFilterMusic{},
	"pinned":     &tg.InputMessagesFilterPinned{},
	"mentions":   &tg.InputMessagesFilterMyMentions{},
}

// FilterNames lists the values accepted by --filter.
func FilterNames() []string {
	var n []string
	for k := range searchFilters {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func (c *Conn) Search(ctx context.Context, o SearchOpts) (*SearchResult, error) {
	if o.Limit <= 0 {
		o.Limit = 20
	}
	if o.Limit > 100 {
		o.Limit = 100
	}
	var filter tg.MessagesFilterClass = &tg.InputMessagesFilterEmpty{}
	if o.Filter != "" {
		f, ok := searchFilters[o.Filter]
		if !ok {
			return nil, usagef("--filter must be one of %s", strings.Join(FilterNames(), ", "))
		}
		filter = f
	}
	minDate, maxDate := 0, 0
	if !o.Since.IsZero() {
		minDate = int(o.Since.Unix())
	}
	if !o.Until.IsZero() {
		maxDate = int(o.Until.Unix())
	}
	var res tg.MessagesMessagesClass
	var err error
	withChat := o.Peer == nil
	if o.Peer != nil {
		req := &tg.MessagesSearchRequest{Peer: o.Peer.Input(), Q: o.Query, Filter: filter, MinDate: minDate, MaxDate: maxDate, OffsetID: o.Before, Limit: o.Limit}
		if o.From != nil {
			req.SetFromID(o.From.Input())
		}
		res, err = c.API.MessagesSearch(ctx, req)
	} else {
		if o.From != nil {
			return nil, usagef("--from needs --peer (Telegram can only filter by sender inside one chat)")
		}
		res, err = c.API.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{
			Q: o.Query, Filter: filter, MinDate: minDate, MaxDate: maxDate, OffsetPeer: &tg.InputPeerEmpty{}, OffsetID: o.Before, Limit: o.Limit,
		})
	}
	if err != nil {
		return nil, wrapRPC(err)
	}
	m, ok := res.AsModified()
	if !ok {
		return &SearchResult{Messages: []Message{}, Untrusted: true}, nil
	}
	c.Peers.Apply(m.GetUsers(), m.GetChats())
	e := newEnts(c.Self.ID, m.GetUsers(), m.GetChats())
	out := &SearchResult{Messages: []Message{}, Untrusted: true}
	switch t := res.(type) {
	case *tg.MessagesMessagesSlice:
		out.Total = t.Count
	case *tg.MessagesChannelMessages:
		out.Total = t.Count
	}
	for _, mc := range m.GetMessages() {
		if v, ok := e.message(mc, withChat); ok {
			out.Messages = append(out.Messages, v)
		}
	}
	return out, nil
}

type InboxOpts struct {
	MaxChats     int
	PerChat      int
	Scan         int
	IncludeMuted bool
	MarkRead     bool
}

type InboxChat struct {
	Chat       Who       `json:"chat"`
	Unread     int       `json:"unread"`
	Messages   []Message `json:"messages"`
	MoreUnread int       `json:"more_unread,omitempty"`
	MarkedRead bool      `json:"marked_read,omitempty"`
}

type Inbox struct {
	Chats     []InboxChat `json:"chats"`
	Untrusted bool        `json:"untrusted"`
}

// Inbox returns unread messages using Telegram's own read markers, so it needs
// no local state: whatever the user has not read in any client shows up here.
func (c *Conn) Inbox(ctx context.Context, o InboxOpts) (*Inbox, error) {
	if o.MaxChats <= 0 {
		o.MaxChats = 10
	}
	if o.PerChat <= 0 {
		o.PerChat = 20
	}
	if o.Scan <= 0 {
		o.Scan = 100
	}
	var unread []Dialog
	scanned := 0
	err := c.walkDialogs(ctx, 0, func(d Dialog) bool {
		scanned++
		if (d.Unread > 0 || d.UnreadMentions > 0) && (o.IncludeMuted || !d.Muted || d.UnreadMentions > 0) {
			unread = append(unread, d)
		}
		return len(unread) < o.MaxChats && scanned < o.Scan
	})
	if err != nil {
		return nil, err
	}
	out := &Inbox{Chats: []InboxChat{}, Untrusted: true}
	for _, d := range unread {
		if d.peer.ID == 0 {
			continue
		}
		n := d.Unread
		if n == 0 {
			n = 1
		}
		limit := n
		if limit > o.PerChat {
			limit = o.PerChat
		}
		msgs, _, err := c.fetchHistory(ctx, d.peer, HistoryOpts{Limit: limit, MinID: d.readInboxMaxID})
		if err != nil {
			return nil, err
		}
		ic := InboxChat{Chat: d.peer.Chat(), Unread: d.Unread, Messages: msgs}
		if d.Title != "" {
			ic.Chat.Name = d.Title
		}
		if d.Unread > len(msgs) {
			ic.MoreUnread = d.Unread - len(msgs)
		}
		if o.MarkRead && ic.MoreUnread == 0 && len(msgs) > 0 {
			if err := c.MarkRead(ctx, d.peer, msgs[len(msgs)-1].ID); err != nil {
				return nil, err
			}
			ic.MarkedRead = true
		}
		out.Chats = append(out.Chats, ic)
	}
	return out, nil
}

func (c *Conn) MarkRead(ctx context.Context, p PeerEntry, maxID int) error {
	if p.Type == "channel" {
		_, err := c.API.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: p.InputChannel(), MaxID: maxID})
		return wrapRPCOrNil(err)
	}
	_, err := c.API.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: p.Input(), MaxID: maxID})
	return wrapRPCOrNil(err)
}

func wrapRPCOrNil(err error) error {
	if err == nil {
		return nil
	}
	return wrapRPC(err)
}

// Info describes one peer.
func (c *Conn) Info(ctx context.Context, p PeerEntry) (map[string]any, error) {
	out := map[string]any{"id": PublicID(p.Type, p.ID), "type": p.Chat().Type, "name": p.Title}
	if p.Username != "" {
		out["username"] = p.Username
	}
	switch p.Type {
	case "user":
		f, err := c.API.UsersGetFullUser(ctx, p.InputUser())
		if err != nil {
			return nil, wrapRPC(err)
		}
		c.Peers.Apply(f.Users, f.Chats)
		if a, ok := f.FullUser.GetAbout(); ok && a != "" {
			out["about"] = a
		}
		for _, uc := range f.Users {
			if u, ok := uc.(*tg.User); ok && u.ID == p.ID {
				if u.Bot {
					out["type"] = "bot"
				}
				if u.Phone != "" {
					out["phone"] = "+" + u.Phone
				}
				if u.Verified {
					out["verified"] = true
				}
				if u.Contact {
					out["contact"] = true
				}
			}
		}
	case "channel":
		f, err := c.API.ChannelsGetFullChannel(ctx, p.InputChannel())
		if err != nil {
			return nil, wrapRPC(err)
		}
		c.Peers.Apply(f.Users, f.Chats)
		if ch, ok := f.FullChat.(*tg.ChannelFull); ok {
			if ch.About != "" {
				out["about"] = ch.About
			}
			if n, ok := ch.GetParticipantsCount(); ok {
				out["members"] = n
			}
		}
		for _, cc := range f.Chats {
			if ch, ok := cc.(*tg.Channel); ok && ch.ID == p.ID {
				out["type"] = chatType(ch)
			}
		}
	case "chat":
		f, err := c.API.MessagesGetFullChat(ctx, p.ID)
		if err != nil {
			return nil, wrapRPC(err)
		}
		c.Peers.Apply(f.Users, f.Chats)
		if ch, ok := f.FullChat.(*tg.ChatFull); ok && ch.About != "" {
			out["about"] = ch.About
		}
	}
	return out, nil
}

type SendOpts struct {
	Text      string
	ReplyTo   int
	Silent    bool
	NoPreview bool
	Parse     string // "", "html", "md"
	File      string
	AsFile    bool
	Schedule  time.Time
}

type Sent struct {
	ID   int    `json:"id,omitempty"`
	Date string `json:"date,omitempty"`
	Chat Who    `json:"chat"`
}

func styled(parse, text string) ([]styling.StyledTextOption, error) {
	switch parse {
	case "":
		return []styling.StyledTextOption{styling.Plain(text)}, nil
	case "html":
		return []styling.StyledTextOption{html.String(nil, text)}, nil
	case "md", "markdown":
		return []styling.StyledTextOption{markdown.String(nil, text)}, nil
	}
	return nil, usagef("--parse must be html or md")
}

func (c *Conn) Send(ctx context.Context, p PeerEntry, o SendOpts) (*Sent, error) {
	rb := message.NewSender(c.API).To(p.Input())
	b := &rb.Builder
	if o.ReplyTo > 0 {
		b = b.Reply(o.ReplyTo)
	}
	if o.Silent {
		b = b.Silent()
	}
	if o.NoPreview {
		b = b.NoWebpage()
	}
	if !o.Schedule.IsZero() {
		b = b.Schedule(o.Schedule)
	}
	text, err := styled(o.Parse, o.Text)
	if err != nil {
		return nil, err
	}
	var upd tg.UpdatesClass
	if o.File == "" {
		if strings.TrimSpace(o.Text) == "" {
			return nil, usagef("nothing to send: give text or --file")
		}
		upd, err = b.StyledText(ctx, text...)
	} else {
		if _, statErr := os.Stat(o.File); statErr != nil {
			return nil, usagef("--file: %v", statErr)
		}
		f, upErr := uploader.NewUploader(c.API).FromPath(ctx, o.File)
		if upErr != nil {
			return nil, wrapRPC(upErr)
		}
		if o.Text == "" {
			text = nil
		}
		ext := strings.ToLower(filepath.Ext(o.File))
		mt := mime.TypeByExtension(ext)
		switch {
		case !o.AsFile && (ext == ".jpg" || ext == ".jpeg" || ext == ".png"):
			upd, err = b.Media(ctx, message.UploadedPhoto(f, text...))
		case !o.AsFile && (ext == ".mp4" || ext == ".mov" || ext == ".webm" || ext == ".mkv"):
			doc := message.UploadedDocument(f, text...)
			if mt != "" {
				doc.MIME(mt)
			}
			upd, err = b.Media(ctx, doc.Video().SupportsStreaming())
		default:
			doc := message.UploadedDocument(f, text...).Filename(filepath.Base(o.File)).ForceFile(true)
			if mt != "" {
				doc.MIME(mt)
			}
			upd, err = b.Media(ctx, doc)
		}
	}
	if err != nil {
		return nil, wrapRPC(err)
	}
	id, date := sentFromUpdates(upd)
	return &Sent{ID: id, Date: rfc3339(date), Chat: p.Chat()}, nil
}

func sentFromUpdates(u tg.UpdatesClass) (id, date int) {
	switch t := u.(type) {
	case *tg.UpdateShortSentMessage:
		return t.ID, t.Date
	case *tg.Updates:
		for _, up := range t.Updates {
			switch x := up.(type) {
			case *tg.UpdateMessageID:
				id = x.ID
			case *tg.UpdateNewMessage:
				if m, ok := x.Message.(*tg.Message); ok {
					return m.ID, m.Date
				}
			case *tg.UpdateNewChannelMessage:
				if m, ok := x.Message.(*tg.Message); ok {
					return m.ID, m.Date
				}
			case *tg.UpdateNewScheduledMessage:
				if m, ok := x.Message.(*tg.Message); ok {
					return m.ID, m.Date
				}
			}
		}
	}
	return id, date
}

func (c *Conn) Edit(ctx context.Context, p PeerEntry, id int, text, parse string) (*Sent, error) {
	opts, err := styled(parse, text)
	if err != nil {
		return nil, err
	}
	if _, err := message.NewSender(c.API).To(p.Input()).Edit(id).StyledText(ctx, opts...); err != nil {
		return nil, wrapRPC(err)
	}
	return &Sent{ID: id, Chat: p.Chat()}, nil
}

func (c *Conn) Delete(ctx context.Context, p PeerEntry, ids []int, revoke bool) error {
	if p.Type == "channel" {
		_, err := c.API.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: p.InputChannel(), ID: ids})
		return wrapRPCOrNil(err)
	}
	_, err := c.API.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{ID: ids, Revoke: revoke})
	return wrapRPCOrNil(err)
}

func (c *Conn) Forward(ctx context.Context, from, to PeerEntry, ids []int) error {
	if len(ids) == 0 {
		return usagef("no message ids")
	}
	_, err := message.NewSender(c.API).To(to.Input()).ForwardIDs(from.Input(), ids[0], ids[1:]...).Send(ctx)
	return wrapRPCOrNil(err)
}

func (c *Conn) getMessage(ctx context.Context, p PeerEntry, id int) (tg.MessageClass, error) {
	in := []tg.InputMessageClass{&tg.InputMessageID{ID: id}}
	var res tg.MessagesMessagesClass
	var err error
	if p.Type == "channel" {
		res, err = c.API.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: p.InputChannel(), ID: in})
	} else {
		res, err = c.API.MessagesGetMessages(ctx, in)
	}
	if err != nil {
		return nil, wrapRPC(err)
	}
	m, ok := res.AsModified()
	if !ok || len(m.GetMessages()) == 0 {
		return nil, usagef("message %d not found", id)
	}
	c.Peers.Apply(m.GetUsers(), m.GetChats())
	return m.GetMessages()[0], nil
}

// Download saves the media of a message and returns the file path. out may be
// a directory or a file path; empty means the current directory.
func (c *Conn) Download(ctx context.Context, p PeerEntry, id int, out string) (string, error) {
	mc, err := c.getMessage(ctx, p, id)
	if err != nil {
		return "", err
	}
	m, ok := mc.(*tg.Message)
	if !ok || m.Media == nil {
		return "", usagef("message %d has no downloadable media", id)
	}
	var loc tg.InputFileLocationClass
	name := fmt.Sprintf("%d_%d", PublicID(p.Type, p.ID), id)
	switch md := m.Media.(type) {
	case *tg.MessageMediaPhoto:
		ph, ok := md.Photo.(*tg.Photo)
		if !ok {
			return "", usagef("the photo is no longer available")
		}
		size := largestPhotoSize(ph.Sizes)
		if size == "" {
			return "", usagef("the photo has no downloadable size")
		}
		loc = &tg.InputPhotoFileLocation{ID: ph.ID, AccessHash: ph.AccessHash, FileReference: ph.FileReference, ThumbSize: size}
		name += ".jpg"
	case *tg.MessageMediaDocument:
		d, ok := md.Document.(*tg.Document)
		if !ok {
			return "", usagef("the document is no longer available")
		}
		loc = &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference}
		fn := ""
		for _, a := range d.Attributes {
			if f, ok := a.(*tg.DocumentAttributeFilename); ok {
				fn = filepath.Base(f.FileName)
			}
		}
		if fn == "" {
			exts, _ := mime.ExtensionsByType(d.MimeType)
			fn = name
			if len(exts) > 0 {
				fn += exts[0]
			}
		}
		name = fn
	default:
		return "", usagef("this message has no downloadable media (type %s)", mediaView(m.Media).Type)
	}
	path := name
	if out != "" {
		st, statErr := os.Stat(out)
		if strings.HasSuffix(out, "/") || strings.HasSuffix(out, string(os.PathSeparator)) || (statErr == nil && st.IsDir()) {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return "", runtimeErr(err)
			}
			path = filepath.Join(out, name)
		} else {
			path = out
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", runtimeErr(err)
			}
		}
	}
	if _, err := downloader.NewDownloader().Download(c.API, loc).ToPath(ctx, path); err != nil {
		return "", wrapRPC(err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, nil
	}
	return abs, nil
}

func largestPhotoSize(sizes []tg.PhotoSizeClass) string {
	best, bestArea := "", -1
	for _, s := range sizes {
		switch t := s.(type) {
		case *tg.PhotoSize:
			if a := t.W * t.H; a > bestArea {
				best, bestArea = t.Type, a
			}
		case *tg.PhotoSizeProgressive:
			if a := t.W * t.H; a > bestArea {
				best, bestArea = t.Type, a
			}
		}
	}
	return best
}

var inviteRe = regexp.MustCompile(`(?:t|telegram)\.me/(?:\+|joinchat/)([A-Za-z0-9_-]+)`)

func (c *Conn) Join(ctx context.Context, ref string) (*Who, error) {
	if m := inviteRe.FindStringSubmatch(ref); m != nil {
		res, err := c.API.MessagesImportChatInvite(ctx, m[1])
		if err != nil {
			return nil, wrapRPC(err)
		}
		ok0, _ := res.(*tg.MessagesChatInviteJoinResultOk)
		var u *tg.Updates
		if ok0 != nil {
			u, _ = ok0.Updates.(*tg.Updates)
		}
		if u != nil {
			c.Peers.Apply(u.Users, u.Chats)
			for _, ch := range u.Chats {
				switch t := ch.(type) {
				case *tg.Channel:
					w := PeerEntry{Type: "channel", ID: t.ID, Title: t.Title, Username: t.Username}.Chat()
					return &w, nil
				case *tg.Chat:
					w := PeerEntry{Type: "chat", ID: t.ID, Title: t.Title}.Chat()
					return &w, nil
				}
			}
		}
		return &Who{}, nil
	}
	p, err := c.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	if p.Type != "channel" {
		return nil, usagef("only channels, supergroups and invite links can be joined")
	}
	if _, err := c.API.ChannelsJoinChannel(ctx, p.InputChannel()); err != nil {
		return nil, wrapRPC(err)
	}
	w := p.Chat()
	return &w, nil
}

func (c *Conn) Leave(ctx context.Context, p PeerEntry) error {
	switch p.Type {
	case "channel":
		_, err := c.API.ChannelsLeaveChannel(ctx, p.InputChannel())
		return wrapRPCOrNil(err)
	case "chat":
		_, err := c.API.MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{ChatID: p.ID, UserID: &tg.InputUserSelf{}})
		return wrapRPCOrNil(err)
	}
	return usagef("a private chat cannot be left")
}
