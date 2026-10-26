// Package cli wires the tgbot command tree.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"
	"github.com/swtormy/tgbot-cli/internal/spec"
	"github.com/swtormy/tgbot-cli/internal/tg"
)

// Exit codes: 0 ok, 1 runtime/API failure, 2 usage error, 3 token missing or rejected.
const (
	exitRuntime = 1
	exitUsage   = 2
	exitAuth    = 3
)

type exitError struct {
	code int
	kind string
	err  error
	api  *tg.APIError
}

func (e *exitError) Error() string { return e.err.Error() }

func usageErr(format string, a ...any) error {
	return &exitError{code: exitUsage, kind: "usage", err: fmt.Errorf(format, a...)}
}

func runtimeErr(err error) error {
	return &exitError{code: exitRuntime, kind: "runtime", err: err}
}

// Version is set at build time.
var Version = "dev"

type app struct {
	spec    *spec.Spec
	in      io.Reader
	out     io.Writer
	errOut  io.Writer
	getenv  func(string) string
	dryRun  bool
	jq      string
	pretty  bool
	apiURL  string
	timeout time.Duration
	usage   bool
	token   string
}

// Execute runs the CLI and returns the process exit code.
func Execute(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	sp, err := spec.Load()
	if err != nil {
		fmt.Fprintln(stderr, "internal error: cannot load embedded spec:", err)
		return exitRuntime
	}
	a := &app{spec: sp, in: stdin, out: stdout, errOut: stderr, getenv: getenv}
	root := a.rootCmd(args)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		return a.reportError(err)
	}
	return 0
}

func (a *app) reportError(err error) int {
	ee, ok := err.(*exitError)
	if !ok {
		ee = &exitError{code: exitUsage, kind: "usage", err: err}
	}
	detail := map[string]any{"type": ee.kind, "message": a.redact(ee.err.Error())}
	if ee.api != nil {
		detail["code"] = ee.api.Code
		detail["message"] = ee.api.Description
		if len(ee.api.Parameters) > 0 {
			detail["parameters"] = ee.api.Parameters
		}
	}
	b, _ := marshal(struct {
		OK    bool           `json:"ok"`
		Error map[string]any `json:"error"`
	}{false, detail})
	fmt.Fprintln(a.errOut, string(b))
	return ee.code
}

func (a *app) redact(s string) string {
	return (&tg.Client{Token: a.token}).Redact(s)
}

func (a *app) client(extraTimeout time.Duration) (*tg.Client, error) {
	a.token = a.getenv("TELEGRAM_BOT_TOKEN")
	if a.token == "" {
		return nil, &exitError{code: exitAuth, kind: "auth", err: errors.New("TELEGRAM_BOT_TOKEN is not set (get a token from @BotFather)")}
	}
	base := a.apiURL
	if base == "" {
		base = a.getenv("TELEGRAM_API_URL")
	}
	return &tg.Client{
		Token:   a.token,
		BaseURL: base,
		HTTP:    &http.Client{Timeout: a.timeout + extraTimeout},
	}, nil
}

// call sends a prepared request, honoring --dry-run, and prints the result.
func (a *app) call(ctx context.Context, r tg.Request, extraTimeout time.Duration) error {
	if a.dryRun {
		return a.emitValue(tg.DryRun(a.apiBase(), r))
	}
	res, err := a.callRaw(ctx, r, extraTimeout)
	if err != nil {
		return err
	}
	return a.emit(res)
}

func (a *app) apiBase() string {
	if a.apiURL != "" {
		return a.apiURL
	}
	return a.getenv("TELEGRAM_API_URL")
}

func (a *app) callRaw(ctx context.Context, r tg.Request, extraTimeout time.Duration) (json.RawMessage, error) {
	c, err := a.client(extraTimeout)
	if err != nil {
		return nil, err
	}
	res, err := c.Call(ctx, r)
	if err != nil {
		return nil, a.wrapCallErr(err)
	}
	return res, nil
}

func (a *app) wrapCallErr(err error) error {
	var api *tg.APIError
	if errors.As(err, &api) {
		code := exitRuntime
		kind := "api"
		if api.IsAuth() {
			code, kind = exitAuth, "auth"
		}
		return &exitError{code: code, kind: kind, err: api, api: api}
	}
	return runtimeErr(err)
}

func (a *app) rootCmd(args []string) *cobra.Command {
	root := &cobra.Command{
		Use:           "tgbot",
		Short:         "Telegram Bot API from the shell, built for AI agents",
		Long:          rootLong,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.usage {
				return a.printUsage()
			}
			return cmd.Help()
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageErr("%s", err)
	})
	pf := root.PersistentFlags()
	pf.BoolVar(&a.dryRun, "dry-run", false, "validate input and print the request instead of sending it (needs no token)")
	pf.StringVar(&a.jq, "jq", "", "filter the result with a jq expression; strings print raw")
	pf.BoolVar(&a.pretty, "pretty", false, "indent JSON output")
	pf.StringVar(&a.apiURL, "api-url", "", "Bot API base URL (default https://api.telegram.org, or $TELEGRAM_API_URL)")
	pf.DurationVar(&a.timeout, "http-timeout", 30*time.Second, "HTTP timeout per request")
	root.Flags().BoolVar(&a.usage, "usage", false, "print every method and its parameters as compact JSON")

	root.AddCommand(a.sendCmd(), a.updatesCmd(), a.downloadCmd(), a.methodsCmd(), a.skillCmd())
	// Method commands are built only when named in the arguments: 185 commands
	// would clutter --help and slow every start.
	for _, arg := range args {
		if m, ok := a.spec.Methods[arg]; ok {
			root.AddCommand(a.methodCmd(m, m.Name, nil))
		}
	}
	return root
}

const rootLong = `tgbot calls the Telegram Bot API from the shell. One binary, JSON in and out.

Setup
  export TELEGRAM_BOT_TOKEN=123456:ABC...     token from @BotFather

Discover (no token needed)
  tgbot methods                list every Bot API method
  tgbot <method> --help        parameters of one method
  tgbot <method> --schema      JSON Schema of the request body
  tgbot --usage                whole command tree as compact JSON

Call
  tgbot send 123456789 "hello"
  tgbot sendPhoto --chat_id 123456789 --photo @cat.jpg --caption "meow"
  tgbot getUpdates --timeout 30
  tgbot sendMessage --body '{"chat_id":123456789,"text":"hi"}'

Preview any call with --dry-run. Flags use the exact parameter names from the
Bot API docs (snake_case). Objects and arrays are passed as JSON strings, and
@file reads JSON from a file. For file parameters, @path uploads a local file.

Output: the result as JSON on stdout. Errors are one JSON line on stderr.
Exit codes: 0 ok, 1 API or runtime failure, 2 usage error, 3 token missing or rejected.`
