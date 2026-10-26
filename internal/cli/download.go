package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/swtormy/tgbot-cli/internal/tg"
)

func (a *app) downloadCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "download <file_id>",
		Short: "Download a file by file_id and print the local path (bots can fetch files up to 20 MB)",
		Example: `  tgbot download AgACAgIAAxkBAAI... --out ./photo.jpg
  tgbot download AgACAgIAAxkBAAI... --out ./downloads/`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := tg.Request{Method: "getFile", Params: map[string]any{"file_id": args[0]}}
			if a.dryRun {
				return a.emitValue(tg.DryRun(a.apiBase(), req))
			}
			res, err := a.callRaw(ctxOf(cmd), req, 0)
			if err != nil {
				return err
			}
			var file struct {
				FilePath string `json:"file_path"`
			}
			if err := json.Unmarshal(res, &file); err != nil || file.FilePath == "" {
				return runtimeErr(fmt.Errorf("getFile returned no file_path"))
			}
			dst := path.Base(file.FilePath)
			if out != "" {
				dst = out
				if st, err := os.Stat(out); err == nil && st.IsDir() {
					dst = filepath.Join(out, path.Base(file.FilePath))
				}
			}
			c, err := a.client(0)
			if err != nil {
				return err
			}
			if err := c.Download(ctxOf(cmd), file.FilePath, dst); err != nil {
				return a.wrapCallErr(err)
			}
			fmt.Fprintln(a.out, dst)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "destination file or directory (default: current directory)")
	return cmd
}
