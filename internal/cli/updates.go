package cli

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/swtormy/tgbot-cli/internal/tg"
)

// updatesCmd wraps getUpdates and reports the offset to pass on the next call,
// so a stateless caller does not have to compute it.
func (a *app) updatesCmd() *cobra.Command {
	var (
		offset    int64
		timeout   int64
		limit     int64
		allowed   string
		stateFile string
	)
	cmd := &cobra.Command{
		Use:   "updates",
		Short: "Fetch new updates and print them with the next offset (wraps getUpdates)",
		Long: `Fetch incoming updates with getUpdates. Prints {"next_offset":N,"updates":[...]}.
Pass next_offset as --offset on the following call to confirm what you already
processed. With --state-file the offset is read from and saved to that file,
so repeated calls only return new updates (an update is marked as handled as
soon as it is printed).

Note: getUpdates does not work while a webhook is set (see deleteWebhook).`,
		Example: `  tgbot updates --timeout 30
  tgbot updates --state-file ~/.tgbot-offset --jq '.updates[].message.text'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			params := map[string]any{}
			if fl.Changed("offset") {
				params["offset"] = offset
			} else if stateFile != "" {
				if b, err := os.ReadFile(stateFile); err == nil {
					n, perr := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
					if perr != nil {
						return usageErr("--state-file %s does not contain an integer", stateFile)
					}
					params["offset"] = n
				}
			}
			if fl.Changed("timeout") {
				params["timeout"] = timeout
			}
			if fl.Changed("limit") {
				params["limit"] = limit
			}
			if allowed != "" {
				f, _ := fieldByName(a.spec.Methods["getUpdates"], "allowed_updates")
				v, _, err := coerce(f, allowed)
				if err != nil {
					return usageErr("%v", err)
				}
				params["allowed_updates"] = v
			}
			req := tg.Request{Method: "getUpdates", Params: params}
			if a.dryRun {
				return a.emitValue(tg.DryRun(a.apiBase(), req))
			}
			res, err := a.callRaw(ctxOf(cmd), req, time.Duration(timeout)*time.Second)
			if err != nil {
				return err
			}
			var updates []json.RawMessage
			if err := json.Unmarshal(res, &updates); err != nil {
				return runtimeErr(err)
			}
			next, hasNext := int64(0), false
			if v, ok := params["offset"].(int64); ok {
				next, hasNext = v, true
			}
			for _, u := range updates {
				var id struct {
					UpdateID int64 `json:"update_id"`
				}
				if err := json.Unmarshal(u, &id); err != nil {
					return runtimeErr(err)
				}
				if id.UpdateID+1 > next {
					next, hasNext = id.UpdateID+1, true
				}
			}
			out := map[string]any{"updates": updates}
			if updates == nil {
				out["updates"] = []json.RawMessage{}
			}
			if hasNext {
				out["next_offset"] = next
			}
			if err := a.emitValue(out); err != nil {
				return err
			}
			if stateFile != "" && hasNext {
				if err := os.WriteFile(stateFile, []byte(strconv.FormatInt(next, 10)+"\n"), 0o600); err != nil {
					return runtimeErr(err)
				}
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.Int64Var(&offset, "offset", 0, "first update_id to return; confirms everything before it")
	fl.Int64Var(&timeout, "timeout", 0, "long-poll seconds (0 returns immediately)")
	fl.Int64Var(&limit, "limit", 0, "max updates to return, 1-100")
	fl.StringVar(&allowed, "allowed_updates", "", "JSON array of update types to receive")
	fl.StringVar(&stateFile, "state-file", "", "file that stores the next offset between calls")
	return cmd
}
