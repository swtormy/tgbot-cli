package mtp

import (
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// Who identifies a person or chat in output.
type Who struct {
	ID       int64  `json:"id"`
	Type     string `json:"type,omitempty"`
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
}

type Media struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Mime     string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Duration int    `json:"duration,omitempty"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
}

// Message is the compact view of a message that agents see.
type Message struct {
	ID      int      `json:"id"`
	Date    string   `json:"date"`
	Chat    *Who     `json:"chat,omitempty"`
	From    *Who     `json:"from,omitempty"`
	Text    string   `json:"text,omitempty"`
	Out     bool     `json:"out,omitempty"`
	ReplyTo int      `json:"reply_to,omitempty"`
	FwdFrom string   `json:"forwarded_from,omitempty"`
	Media   *Media   `json:"media,omitempty"`
	Links   []string `json:"links,omitempty"`
	Edited  string   `json:"edited,omitempty"`
	Views   int      `json:"views,omitempty"`
	Pinned  bool     `json:"pinned,omitempty"`
	Service string   `json:"service,omitempty"`
	AlbumID int64    `json:"album_id,omitempty"`
}

// Dialog is one chat in the chat list.
type Dialog struct {
	ID             int64    `json:"id"`
	Type           string   `json:"type"`
	Title          string   `json:"title"`
	Username       string   `json:"username,omitempty"`
	Unread         int      `json:"unread,omitempty"`
	UnreadMentions int      `json:"unread_mentions,omitempty"`
	Muted          bool     `json:"muted,omitempty"`
	Pinned         bool     `json:"pinned,omitempty"`
	Archived       bool     `json:"archived,omitempty"`
	LastMessage    *Message `json:"last_message,omitempty"`

	readInboxMaxID int
	peer           PeerEntry
}

// ents looks up the users and chats that come with an API response.
type ents struct {
	users map[int64]*tg.User
	chats map[int64]tg.ChatClass
	self  int64
}

func newEnts(self int64, users []tg.UserClass, chats []tg.ChatClass) *ents {
	e := &ents{users: map[int64]*tg.User{}, chats: map[int64]tg.ChatClass{}, self: self}
	for _, u := range users {
		if uu, ok := u.(*tg.User); ok {
			e.users[uu.ID] = uu
		}
	}
	for _, c := range chats {
		switch cc := c.(type) {
		case *tg.Chat:
			e.chats[cc.ID] = cc
		case *tg.Channel:
			e.chats[cc.ID] = cc
		case *tg.ChannelForbidden:
			e.chats[cc.ID] = cc
		}
	}
	return e
}

func (e *ents) who(p tg.PeerClass) *Who {
	switch t := p.(type) {
	case *tg.PeerUser:
		w := &Who{ID: t.UserID, Type: "user"}
		if u, ok := e.users[t.UserID]; ok {
			w.Name, w.Username = userName(u), u.Username
			if u.Bot {
				w.Type = "bot"
			}
		}
		return w
	case *tg.PeerChat:
		w := &Who{ID: PublicID("chat", t.ChatID), Type: "group"}
		if c, ok := e.chats[t.ChatID].(*tg.Chat); ok {
			w.Name = c.Title
		}
		return w
	case *tg.PeerChannel:
		w := &Who{ID: PublicID("channel", t.ChannelID), Type: "channel"}
		switch c := e.chats[t.ChannelID].(type) {
		case *tg.Channel:
			w.Name, w.Username = c.Title, c.Username
			if c.Megagroup {
				w.Type = "supergroup"
			}
		case *tg.ChannelForbidden:
			w.Name = c.Title
		}
		return w
	}
	return nil
}

func chatType(c tg.ChatClass) string {
	if ch, ok := c.(*tg.Channel); ok && ch.Megagroup {
		return "supergroup"
	}
	if _, ok := c.(*tg.Chat); ok {
		return "group"
	}
	return "channel"
}

func rfc3339(ts int) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(int64(ts), 0).UTC().Format(time.RFC3339)
}

// message converts a raw message. withChat adds the chat, for lists that mix chats.
func (e *ents) message(mc tg.MessageClass, withChat bool) (Message, bool) {
	switch m := mc.(type) {
	case *tg.Message:
		out := Message{ID: m.ID, Date: rfc3339(m.Date), Text: m.Message, Out: m.Out, Pinned: m.Pinned, Views: m.Views, AlbumID: m.GroupedID}
		if withChat {
			out.Chat = e.who(m.PeerID)
		}
		switch {
		case m.FromID != nil:
			out.From = e.who(m.FromID)
		case m.Out:
			out.From = e.who(&tg.PeerUser{UserID: e.self})
		default:
			out.From = e.who(m.PeerID)
		}
		if rt, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok {
			out.ReplyTo = rt.ReplyToMsgID
		}
		if f, ok := m.GetFwdFrom(); ok {
			switch {
			case f.FromName != "":
				out.FwdFrom = f.FromName
			case f.FromID != nil:
				if w := e.who(f.FromID); w != nil {
					out.FwdFrom = firstNonEmpty(w.Name, w.Username)
				}
			}
		}
		if ed, ok := m.GetEditDate(); ok {
			out.Edited = rfc3339(ed)
		}
		if m.Media != nil {
			out.Media = mediaView(m.Media)
		}
		for _, en := range m.Entities {
			if tu, ok := en.(*tg.MessageEntityTextURL); ok {
				out.Links = append(out.Links, tu.URL)
			}
		}
		return out, true
	case *tg.MessageService:
		out := Message{ID: m.ID, Date: rfc3339(m.Date), Out: m.Out, Service: strings.TrimPrefix(fmt.Sprintf("%T", m.Action), "*tg.MessageAction")}
		if withChat {
			out.Chat = e.who(m.PeerID)
		}
		if m.FromID != nil {
			out.From = e.who(m.FromID)
		}
		return out, true
	}
	return Message{}, false
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func mediaView(mc tg.MessageMediaClass) *Media {
	switch m := mc.(type) {
	case *tg.MessageMediaPhoto:
		return &Media{Type: "photo"}
	case *tg.MessageMediaDocument:
		d, ok := m.Document.(*tg.Document)
		if !ok {
			return &Media{Type: "document"}
		}
		v := &Media{Type: "document", Mime: d.MimeType, Size: d.Size}
		for _, a := range d.Attributes {
			switch at := a.(type) {
			case *tg.DocumentAttributeFilename:
				v.Name = at.FileName
			case *tg.DocumentAttributeVideo:
				v.Type, v.Duration = "video", int(at.Duration)
				if at.RoundMessage {
					v.Type = "video_note"
				}
			case *tg.DocumentAttributeAudio:
				v.Type, v.Duration, v.Title = "audio", at.Duration, at.Title
				if at.Voice {
					v.Type = "voice"
				}
			case *tg.DocumentAttributeSticker:
				v.Type = "sticker"
			case *tg.DocumentAttributeAnimated:
				if v.Type == "document" || v.Type == "video" {
					v.Type = "gif"
				}
			}
		}
		return v
	case *tg.MessageMediaWebPage:
		v := &Media{Type: "webpage"}
		if w, ok := m.Webpage.(*tg.WebPage); ok {
			v.URL, v.Title = w.URL, w.Title
		}
		return v
	}
	return &Media{Type: strings.ToLower(strings.TrimPrefix(fmt.Sprintf("%T", mc), "*tg.MessageMedia"))}
}

func messageTime(mc tg.MessageClass) int {
	switch m := mc.(type) {
	case *tg.Message:
		return m.Date
	case *tg.MessageService:
		return m.Date
	}
	return 0
}

func messagePeer(mc tg.MessageClass) tg.PeerClass {
	switch m := mc.(type) {
	case *tg.Message:
		return m.PeerID
	case *tg.MessageService:
		return m.PeerID
	}
	return nil
}

func peerPublicID(p tg.PeerClass) int64 {
	switch t := p.(type) {
	case *tg.PeerUser:
		return t.UserID
	case *tg.PeerChat:
		return PublicID("chat", t.ChatID)
	case *tg.PeerChannel:
		return PublicID("channel", t.ChannelID)
	}
	return 0
}
