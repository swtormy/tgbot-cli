// Package mtp talks to Telegram as a user account over MTProto.
package mtp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tgerr"
)

type Kind string

const (
	KindUsage   Kind = "usage"   // bad arguments
	KindAuth    Kind = "auth"    // no session, or the session is dead
	KindBlocked Kind = "blocked" // refused by the access policy
	KindAPI     Kind = "api"     // Telegram returned an error
	KindRuntime Kind = "runtime" // network, disk, anything else
)

// Error is a failure with enough structure for the CLI to pick an exit code.
type Error struct {
	Kind    Kind
	Code    int
	Message string
	Params  map[string]any
	Err     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

func usagef(format string, a ...any) error {
	return &Error{Kind: KindUsage, Message: fmt.Sprintf(format, a...)}
}

func runtimeErr(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	return wrapRPC(err)
}

var deadSession = map[string]string{
	"AUTH_KEY_UNREGISTERED":   "the session is not authorized (it was never completed, or it was terminated)",
	"AUTH_KEY_INVALID":        "the session key is invalid",
	"AUTH_KEY_DUPLICATED":     "the same session was used from two places at once, so Telegram invalidated it",
	"SESSION_REVOKED":         "the session was terminated (from Telegram > Settings > Devices, or by a password change)",
	"SESSION_EXPIRED":         "the session expired",
	"USER_DEACTIVATED":        "the account was deleted or deactivated",
	"USER_DEACTIVATED_BAN":    "the account was banned by Telegram",
	"SESSION_PASSWORD_NEEDED": "this account has two-step verification enabled",
}

// wrapRPC converts a Telegram RPC error into *Error.
func wrapRPC(err error) error {
	var own *Error
	if errors.As(err, &own) {
		return err
	}
	rpc, ok := tgerr.As(err)
	if !ok {
		return &Error{Kind: KindRuntime, Message: err.Error(), Err: err}
	}
	if why, dead := deadSession[rpc.Type]; dead && rpc.Type != "SESSION_PASSWORD_NEEDED" {
		return &Error{
			Kind:    KindAuth,
			Code:    rpc.Code,
			Message: rpc.Type + ": " + why + ". Run `tgbot user login` again.",
			Err:     err,
		}
	}
	e := &Error{Kind: KindAPI, Code: rpc.Code, Message: rpc.Type, Err: err}
	if rpc.Argument != 0 {
		e.Params = map[string]any{"argument": rpc.Argument}
		if strings.HasPrefix(rpc.Type, "FLOOD_WAIT") || rpc.Type == "SLOWMODE_WAIT" || rpc.Type == "FLOOD_PREMIUM_WAIT" {
			e.Params = map[string]any{"retry_after": rpc.Argument}
		}
	}
	return e
}
