package mtp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	"github.com/gotd/td/tg"
)

func TestCatalogParsesFlagsAndVectors(t *testing.T) {
	c := loadCatalog()
	if c.layer == "" {
		t.Fatal("layer not parsed")
	}
	d := c.methods["messages.sendMessage"]
	if d == nil {
		t.Fatal("messages.sendMessage missing")
	}
	byName := map[string]TLField{}
	for _, f := range d.Fields {
		byName[f.Name] = f
	}
	if f := byName["flags"]; !f.Flags {
		t.Errorf("flags word not detected: %+v", f)
	}
	if f := byName["silent"]; !f.Optional || f.Type != "true" || f.FlagName != "flags" {
		t.Errorf("silent: %+v", f)
	}
	if f := byName["peer"]; f.Optional || f.Type != "InputPeer" {
		t.Errorf("peer: %+v", f)
	}
	if f := byName["entities"]; !f.Optional || f.Type != "Vector<MessageEntity>" {
		t.Errorf("entities: %+v", f)
	}
	if d.Result != "Updates" {
		t.Errorf("result = %q", d.Result)
	}
}

func TestEveryMethodHasAConstructorID(t *testing.T) {
	loadIDs()
	for _, n := range MethodNames("") {
		if _, ok := nameID[n]; !ok {
			t.Errorf("no constructor id for method %s", n)
		}
	}
}

func encoded(t *testing.T, e bin.Encoder) []byte {
	t.Helper()
	var b bin.Buffer
	if err := e.Encode(&b); err != nil {
		t.Fatal(err)
	}
	return b.Buf
}

func plan(t *testing.T, method, params string) *RawPlan {
	t.Helper()
	p, err := ParseParams([]byte(params))
	if err != nil {
		t.Fatal(err)
	}
	pl, err := PlanRaw(method, p, PlaceholderResolver)
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

func TestCodecMatchesGotdEncoding(t *testing.T) {
	cases := []struct {
		name   string
		method string
		params string
		want   bin.Encoder
	}{
		{
			"plain", "messages.getHistory",
			`{"peer":{"@type":"inputPeerEmpty"},"limit":5}`,
			&tg.MessagesGetHistoryRequest{Peer: &tg.InputPeerEmpty{}, Limit: 5},
		},
		{
			"flags and optional", "messages.sendMessage",
			`{"peer":{"@type":"inputPeerSelf"},"message":"hi","random_id":42,"silent":true,"reply_to":{"@type":"inputReplyToMessage","reply_to_msg_id":7}}`,
			&tg.MessagesSendMessageRequest{
				Silent: true, Peer: &tg.InputPeerSelf{}, Message: "hi", RandomID: 42,
				ReplyTo: &tg.InputReplyToMessage{ReplyToMsgID: 7},
			},
		},
		{
			"vector of longs", "messages.getMessages",
			`{"id":[{"@type":"inputMessageID","id":1},{"@type":"inputMessageID","id":2}]}`,
			&tg.MessagesGetMessagesRequest{ID: []tg.InputMessageClass{&tg.InputMessageID{ID: 1}, &tg.InputMessageID{ID: 2}}},
		},
		{
			"string", "contacts.resolveUsername",
			`{"username":"durov"}`,
			&tg.ContactsResolveUsernameRequest{Username: "durov"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pl := plan(t, tc.method, tc.params)
			got := encoded(t, pl.Request)
			want := encoded(t, tc.want)
			if !bytes.Equal(got, want) {
				t.Fatalf("encoding differs from gotd\n got %x\nwant %x", got, want)
			}
		})
	}
}

func TestCodecRejectsBadInput(t *testing.T) {
	for _, tc := range []struct{ method, params, want string }{
		{"messages.noSuchThing", `{}`, "unknown MTProto method"},
		{"messages.getHistory", `{"peer":{"@type":"inputPeerEmpty"},"bogus":1}`, "bogus"},
		{"messages.getHistory", `{"peer":"nope"}`, "peer"},
	} {
		p, err := ParseParams([]byte(tc.params))
		if err != nil {
			t.Fatal(err)
		}
		_, err = PlanRaw(tc.method, p, PlaceholderResolver)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %s: err = %v, want containing %q", tc.method, tc.params, err, tc.want)
		}
	}
}

func TestDecodeObjectWithFlags(t *testing.T) {
	loadIDs()
	msg := &tg.Message{ID: 9, PeerID: &tg.PeerUser{UserID: 5}, Date: 1700000000, Message: "hello", Out: true}
	msg.SetFlags()
	var b bin.Buffer
	if err := msg.Encode(&b); err != nil {
		t.Fatal(err)
	}
	v, err := decodeValue(&b, "Message")
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["@type"] != "message" || m["message"] != "hello" || m["id"] != int64(9) || m["out"] != true {
		t.Fatalf("decoded = %v", m)
	}
	if _, has := m["edit_date"]; has {
		t.Errorf("unset optional field present: %v", m)
	}
}

func TestClassify(t *testing.T) {
	want := map[string]string{
		"messages.getHistory":           ClassRead,
		"contacts.search":               ClassRead,
		"users.getFullUser":             ClassRead,
		"messages.sendMessage":          ClassWrite,
		"messages.deleteMessages":       ClassWrite,
		"channels.joinChannel":          ClassWrite,
		"messages.getBotCallbackAnswer": ClassWrite,
		"auth.logOut":                   ClassBlocked,
		"auth.exportLoginToken":         ClassBlocked,
		"account.deleteAccount":         ClassBlocked,
		"payments.sendStarsForm":        ClassBlocked,
	}
	for m, c := range want {
		if got := Classify(m); got != c {
			t.Errorf("Classify(%s) = %s, want %s", m, got, c)
		}
	}
}

func TestBlockedMethodsAreNeverPlanned(t *testing.T) {
	_, err := PlanRaw("account.deleteAccount", map[string]any{"reason": "x"}, PlaceholderResolver)
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindBlocked {
		t.Fatalf("err = %v", err)
	}
}

func TestPublicIDRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		typ string
		id  int64
		pub int64
	}{
		{"user", 777, 777},
		{"chat", 12345, -12345},
		{"channel", 1234567890, -1001234567890},
	} {
		if got := PublicID(tc.typ, tc.id); got != tc.pub {
			t.Errorf("PublicID(%s,%d) = %d, want %d", tc.typ, tc.id, got, tc.pub)
		}
		typ, id := splitPublicID(tc.pub)
		if typ != tc.typ || id != tc.id {
			t.Errorf("splitPublicID(%d) = %s,%d", tc.pub, typ, id)
		}
	}
}

func TestRequireWrite(t *testing.T) {
	p := &Profile{Name: "x"}
	var e *Error
	if err := p.RequireWrite("send"); !errors.As(err, &e) || e.Kind != KindBlocked {
		t.Fatalf("read-only profile allowed a write: %v", err)
	}
	p.Cfg.Access = AccessWrite
	if err := p.RequireWrite("send"); err != nil {
		t.Fatal(err)
	}
}

func TestWrapRPCKeepsOwnErrors(t *testing.T) {
	own := &Error{Kind: KindBlocked, Message: "no"}
	var e *Error
	if err := wrapRPC(own); !errors.As(err, &e) || e.Kind != KindBlocked {
		t.Fatalf("own error was rewritten: %v", err)
	}
}

func TestSessionStorageKeepsBackup(t *testing.T) {
	ctx := context.Background()
	f := &fileStorage{path: filepath.Join(t.TempDir(), "session.json")}
	if _, err := f.LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("empty store: %v", err)
	}
	for _, v := range []string{"one", "two"} {
		if err := f.StoreSession(ctx, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := f.LoadSession(ctx); string(b) != "two" {
		t.Fatalf("content = %q", b)
	}
	// a torn main file falls back to the previous good copy
	if err := os.WriteFile(f.path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := f.LoadSession(ctx); string(b) != "one" {
		t.Fatalf("fallback = %q", b)
	}
}

func TestTransportOrder(t *testing.T) {
	cases := []struct {
		o    Options
		pin  string
		want string
	}{
		{Options{}, "", "tcp,websocket"},
		{Options{Transport: "auto"}, "", "tcp,websocket"},
		{Options{}, "websocket", "websocket,tcp"},
		{Options{Transport: "tcp"}, "websocket", "tcp"},
		{Options{Transport: "websocket"}, "", "websocket"},
		{Options{Proxy: "http://p:8080"}, "", "websocket"},
		{Options{Proxy: "socks5://p:1080"}, "", "tcp,websocket"},
	}
	for _, tc := range cases {
		p := &Profile{Cfg: Config{Transport: tc.pin}}
		got, err := p.transportOrder(tc.o)
		if err != nil || strings.Join(got, ",") != tc.want {
			t.Errorf("%+v pin=%q: got %v, %v; want %s", tc.o, tc.pin, got, err, tc.want)
		}
	}
	p := &Profile{}
	if _, err := p.transportOrder(Options{Transport: "tcp", Proxy: "http://p:1"}); err == nil {
		t.Error("tcp over an http proxy accepted")
	}
	if _, err := p.transportOrder(Options{Transport: "carrier-pigeon"}); err == nil {
		t.Error("bad transport accepted")
	}
}

func TestResolverFor(t *testing.T) {
	for _, tr := range []string{TransportTCP, TransportWebsocket} {
		if _, err := resolverFor(tr, ""); err != nil {
			t.Errorf("%s: %v", tr, err)
		}
	}
	if _, err := resolverFor(TransportTCP, "socks5://127.0.0.1:1080"); err != nil {
		t.Error(err)
	}
	if _, err := resolverFor(TransportWebsocket, "http://127.0.0.1:8080"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"nonsense", "ftp://h:1", "http://h:1"} {
		if _, err := resolverFor(TransportTCP, bad); err == nil {
			t.Errorf("tcp accepted proxy %q", bad)
		}
	}
}
