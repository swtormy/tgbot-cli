package mtp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gotd/td/bin"
)

const (
	ClassRead    = "read"
	ClassWrite   = "write"
	ClassBlocked = "blocked"
)

// Methods that can take over or destroy the account, move money, or change how
// the account is secured. They are refused for every profile, whatever its
// access level, so a prompt-injected agent cannot reach them.
var blockedPrefixes = []string{"auth.", "payments.", "invoke", "initConnection", "test."}

var blockedMethods = map[string]bool{
	"account.deleteAccount":               true,
	"account.resetPassword":               true,
	"account.declinePasswordReset":        true,
	"account.updatePasswordSettings":      true,
	"account.confirmPasswordEmail":        true,
	"account.resendPasswordEmail":         true,
	"account.cancelPasswordEmail":         true,
	"account.getPasswordSettings":         true,
	"account.getTmpPassword":              true,
	"account.sendChangePhoneCode":         true,
	"account.changePhone":                 true,
	"account.sendConfirmPhoneCode":        true,
	"account.confirmPhone":                true,
	"account.sendVerifyPhoneCode":         true,
	"account.verifyPhone":                 true,
	"account.sendVerifyEmailCode":         true,
	"account.verifyEmail":                 true,
	"account.changeAuthorizationSettings": true,
	"account.resetAuthorization":          true,
	"account.setAuthorizationTTL":         true,
	"account.setAccountTTL":               true,
	"account.invalidateSignInCodes":       true,
	"account.initPasskeyRegistration":     true,
	"account.registerPasskey":             true,
	"account.deletePasskey":               true,
	"users.setSecureValueErrors":          true,
	"account.saveSecureValue":             true,
	"account.deleteSecureValue":           true,
	"account.getSecureValue":              true,
	"account.getAuthorizationForm":        true,
	"account.acceptAuthorization":         true,
	"messages.requestEncryption":          true,
	"messages.acceptEncryption":           true,
	"messages.discardEncryption":          true,
	"messages.sendEncrypted":              true,
	"messages.sendEncryptedFile":          true,
	"messages.sendEncryptedService":       true,
	"messages.getDhConfig":                true,
	"help.acceptTermsOfService":           true,
	"help.saveAppLog":                     true,
}

// Names that start with get/search/... but still act on the account.
var writeDespiteGet = map[string]bool{
	"messages.getBotCallbackAnswer": true,
	"messages.requestWebView":       true,
	"messages.requestSimpleWebView": true,
	"messages.requestMainWebView":   true,
	"messages.requestAppWebView":    true,
}

var readPrefixes = []string{"get", "search", "resolve", "check"}

// Classify decides whether a method only reads, changes the account, or is
// never allowed.
func Classify(method string) string {
	for _, p := range blockedPrefixes {
		if strings.HasPrefix(method, p) {
			return ClassBlocked
		}
	}
	if blockedMethods[method] {
		return ClassBlocked
	}
	if writeDespiteGet[method] {
		return ClassWrite
	}
	short := method
	if i := strings.LastIndex(method, "."); i >= 0 {
		short = method[i+1:]
	}
	for _, p := range readPrefixes {
		if strings.HasPrefix(short, p) {
			return ClassRead
		}
	}
	return ClassWrite
}

func suggest(method string) string {
	q := strings.ToLower(method)
	if i := strings.LastIndex(q, "."); i >= 0 {
		q = q[i+1:]
	}
	var hits []string
	for _, n := range MethodNames("") {
		if strings.Contains(strings.ToLower(n), q) {
			hits = append(hits, n)
		}
	}
	sort.Strings(hits)
	if len(hits) > 5 {
		hits = hits[:5]
	}
	if len(hits) == 0 {
		return " (list them with `tgbot user raw list [prefix]`)"
	}
	return " (did you mean " + strings.Join(hits, ", ") + "?)"
}

// RawPlan is a validated request that has not been sent.
type RawPlan struct {
	Method  string
	Class   string
	Request *tlRequest
	Result  string
	Params  map[string]any
}

// PlanRaw validates params against the schema. resolve turns the @peer, @user
// and @channel shorthands into Input* objects.
func PlanRaw(method string, params map[string]any, resolve func(kind, ref string) (map[string]any, error)) (*RawPlan, error) {
	d, ok := loadCatalog().methods[method]
	if !ok {
		return nil, usagef("unknown MTProto method %q%s", method, suggest(method))
	}
	class := Classify(method)
	if class == ClassBlocked {
		return nil, &Error{Kind: KindBlocked, Message: fmt.Sprintf(
			"%s is never allowed through tgbot (it can take over, secure/unsecure or delete the account, or spend money). Use the official Telegram app.", method)}
	}
	if params == nil {
		params = map[string]any{}
	}
	norm, err := expandShorthands(params, resolve)
	if err != nil {
		return nil, err
	}
	m := norm.(map[string]any)
	req := &tlRequest{decl: d, params: m}
	var buf bin.Buffer
	if err := req.Encode(&buf); err != nil {
		return nil, err
	}
	return &RawPlan{Method: method, Class: class, Request: req, Result: d.Result, Params: m}, nil
}

// ParseParams reads a JSON object, keeping numbers exact.
func ParseParams(raw []byte) (map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, usagef("params are not valid JSON: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, usagef("params must be a JSON object")
	}
	return m, nil
}

func expandShorthands(v any, resolve func(kind, ref string) (map[string]any, error)) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 1 {
			for _, kind := range []string{"@peer", "@user", "@channel"} {
				if ref, ok := t[kind]; ok {
					s := fmt.Sprint(ref)
					if n, isNum := ref.(json.Number); isNum {
						s = n.String()
					}
					return resolve(strings.TrimPrefix(kind, "@"), s)
				}
			}
		}
		out := make(map[string]any, len(t))
		for k, x := range t {
			e, err := expandShorthands(x, resolve)
			if err != nil {
				return nil, err
			}
			out[k] = e
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			e, err := expandShorthands(x, resolve)
			if err != nil {
				return nil, err
			}
			out[i] = e
		}
		return out, nil
	}
	return v, nil
}

func (c *Conn) shorthand(ctx context.Context) func(kind, ref string) (map[string]any, error) {
	return func(kind, ref string) (map[string]any, error) {
		p, err := c.Resolve(ctx, ref)
		if err != nil {
			return nil, err
		}
		self := p.Type == "user" && p.ID == c.Self.ID
		switch kind {
		case "peer":
			switch {
			case self:
				return map[string]any{"@type": "inputPeerSelf"}, nil
			case p.Type == "chat":
				return map[string]any{"@type": "inputPeerChat", "chat_id": json.Number(fmt.Sprint(p.ID))}, nil
			case p.Type == "channel":
				return map[string]any{"@type": "inputPeerChannel", "channel_id": json.Number(fmt.Sprint(p.ID)), "access_hash": json.Number(fmt.Sprint(p.AccessHash))}, nil
			}
			return map[string]any{"@type": "inputPeerUser", "user_id": json.Number(fmt.Sprint(p.ID)), "access_hash": json.Number(fmt.Sprint(p.AccessHash))}, nil
		case "user":
			if p.Type != "user" {
				return nil, usagef("%q is not a user", ref)
			}
			if self {
				return map[string]any{"@type": "inputUserSelf"}, nil
			}
			return map[string]any{"@type": "inputUser", "user_id": json.Number(fmt.Sprint(p.ID)), "access_hash": json.Number(fmt.Sprint(p.AccessHash))}, nil
		default:
			if p.Type != "channel" {
				return nil, usagef("%q is not a channel or supergroup", ref)
			}
			return map[string]any{"@type": "inputChannel", "channel_id": json.Number(fmt.Sprint(p.ID)), "access_hash": json.Number(fmt.Sprint(p.AccessHash))}, nil
		}
	}
}

// PlaceholderResolver stands in for real lookups when only validating (--dry-run).
func PlaceholderResolver(kind, ref string) (map[string]any, error) {
	switch kind {
	case "user":
		return map[string]any{"@type": "inputUserEmpty"}, nil
	case "channel":
		return map[string]any{"@type": "inputChannelEmpty"}, nil
	}
	return map[string]any{"@type": "inputPeerEmpty"}, nil
}

// Raw runs any MTProto method. Reads are always allowed; writes need an
// access-write profile; a small set of dangerous methods is never allowed.
func (c *Conn) Raw(ctx context.Context, method string, params map[string]any) (any, error) {
	plan, err := PlanRaw(method, params, c.shorthand(ctx))
	if err != nil {
		return nil, err
	}
	if plan.Class == ClassWrite {
		if err := c.Profile.RequireWrite("`raw " + method + "`"); err != nil {
			return nil, err
		}
	}
	res := &tlResult{typ: plan.Result}
	if err := c.Client.Invoke(ctx, plan.Request, res); err != nil {
		return nil, wrapRPC(err)
	}
	return res.out, nil
}
