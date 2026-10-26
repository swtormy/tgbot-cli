package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/swtormy/tgbot-cli/internal/spec"
	"github.com/swtormy/tgbot-cli/internal/tg"
)

const flagDescLimit = 150

func ctxOf(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}

func (a *app) readStdin() (string, error) {
	b, err := io.ReadAll(a.in)
	if err != nil {
		return "", runtimeErr(err)
	}
	return strings.TrimSuffix(string(b), "\n"), nil
}

func fieldByName(m spec.Method, name string) (spec.Field, bool) {
	for _, f := range m.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return spec.Field{}, false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func flagUsage(f spec.Field) string {
	req := ""
	if f.Required {
		req = "required, "
	}
	return fmt.Sprintf("(%s%s) %s", req, f.TypeLabel(), truncate(f.Description, flagDescLimit))
}

// methodCmd builds a command for one Bot API method. Fields named in
// positional are taken from arguments instead of flags.
func (a *app) methodCmd(m spec.Method, use string, positional []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: m.Short(),
		Long:  methodLong(m),
		Args:  cobra.ExactArgs(len(positional)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runMethod(cmd, m, positional, args)
		},
	}
	fl := cmd.Flags()
	for _, f := range m.Fields {
		if contains(positional, f.Name) {
			continue
		}
		fl.String(f.Name, "", flagUsage(f))
	}
	fl.String("body", "", "full request body as a JSON object (@file or - for stdin); flags override it")
	fl.Bool("schema", false, "print the JSON Schema of the request body and exit")
	return cmd
}

func methodLong(m spec.Method) string {
	var b strings.Builder
	b.WriteString(strings.Join(m.Description, "\n\n"))
	if len(m.Returns) > 0 {
		b.WriteString("\n\nReturns: " + strings.Join(m.Returns, " or "))
	}
	b.WriteString("\nDocs: " + m.Href)
	b.WriteString("\nFull parameter descriptions: tgbot " + m.Name + " --schema")
	return b.String()
}

func (a *app) runMethod(cmd *cobra.Command, m spec.Method, positional, args []string) error {
	fl := cmd.Flags()
	if want, _ := fl.GetBool("schema"); want {
		return a.emitValue(methodSchema(m))
	}

	params := map[string]any{}
	files := map[string]string{}

	if body, _ := fl.GetString("body"); body != "" {
		text := body
		switch {
		case body == "-":
			s, err := a.readStdin()
			if err != nil {
				return err
			}
			text = s
		case strings.HasPrefix(body, "@"):
			b, err := os.ReadFile(body[1:])
			if err != nil {
				return usageErr("--body: %v", err)
			}
			text = string(b)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(text), &obj); err != nil {
			return usageErr("--body must be a JSON object: %v", err)
		}
		for k, v := range obj {
			if _, ok := fieldByName(m, k); !ok {
				return usageErr("--body: %s has no parameter %q (see tgbot %s --schema)", m.Name, k, m.Name)
			}
			params[k] = v
		}
	}

	set := func(f spec.Field, raw string) error {
		v, file, err := coerce(f, raw)
		if err != nil {
			return usageErr("%v", err)
		}
		if file != "" {
			files[f.Name] = file
			delete(params, f.Name)
		} else {
			params[f.Name] = v
			delete(files, f.Name)
		}
		return nil
	}

	for i, name := range positional {
		f, _ := fieldByName(m, name)
		raw := args[i]
		if raw == "-" {
			s, err := a.readStdin()
			if err != nil {
				return err
			}
			raw = s
		}
		if err := set(f, raw); err != nil {
			return err
		}
	}
	for _, f := range m.Fields {
		if contains(positional, f.Name) || !fl.Changed(f.Name) {
			continue
		}
		raw, _ := fl.GetString(f.Name)
		if err := set(f, raw); err != nil {
			return err
		}
	}

	var missing []string
	for _, f := range m.Fields {
		_, inParams := params[f.Name]
		_, inFiles := files[f.Name]
		if f.Required && !inParams && !inFiles {
			missing = append(missing, "--"+f.Name)
		}
	}
	if len(missing) > 0 {
		return usageErr("missing required: %s (see tgbot %s --help)", strings.Join(missing, ", "), m.Name)
	}

	var extra time.Duration
	if t, ok := params["timeout"].(int64); ok && m.Name == "getUpdates" {
		extra = time.Duration(t) * time.Second
	}
	return a.call(ctxOf(cmd), tg.Request{Method: m.Name, Params: params, Files: files}, extra)
}

func (a *app) sendCmd() *cobra.Command {
	m := a.spec.Methods["sendMessage"]
	c := a.methodCmd(m, "send <chat_id> <text>", []string{"chat_id", "text"})
	c.Short = "Send a text message (shortcut for sendMessage; text \"-\" reads stdin)"
	c.Example = `  tgbot send 123456789 "build finished"
  tgbot send @mychannel "<b>deploy</b> ok" --parse_mode HTML
  git log -1 | tgbot send 123456789 -`
	return c
}

func (a *app) methodsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "methods",
		Short: "List every Bot API method",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(a.out, "# %s (%s), %d methods. Details: tgbot <method> --help\n", a.spec.Version, a.spec.ReleaseDate, len(a.spec.Methods))
			for _, n := range a.spec.Names() {
				fmt.Fprintf(a.out, "%s\t%s\n", n, a.spec.Methods[n].Short())
			}
			return nil
		},
	}
}

// printUsage prints the whole method tree as compact JSON. "!" marks required parameters.
func (a *app) printUsage() error {
	methods := map[string]any{}
	for _, n := range a.spec.Names() {
		m := a.spec.Methods[n]
		params := make([]string, 0, len(m.Fields))
		for _, f := range m.Fields {
			bang := ""
			if f.Required {
				bang = "!"
			}
			params = append(params, f.Name+bang+":"+strings.Join(f.Types, "|"))
		}
		methods[n] = map[string]any{"returns": strings.Join(m.Returns, "|"), "params": params}
	}
	return a.emitValue(map[string]any{
		"spec": a.spec.Version,
		"shortcuts": []string{
			"send <chat_id> <text|->",
			"updates [--offset N] [--timeout S] [--limit N] [--state-file F]",
			"download <file_id> [--out PATH]",
			"methods",
			"skill print|install",
		},
		"global_flags": []string{"--dry-run", "--jq EXPR", "--pretty", "--api-url URL", "--http-timeout DUR"},
		"methods":      methods,
	})
}
