package cli

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed SKILL.md
var skillMD string

//go:embed SKILL_USER.md
var skillUserMD string

var skills = map[string]*string{"tgbot": &skillMD, "tgbot-user": &skillUserMD}

func skillNames() []string { return []string{"tgbot", "tgbot-user"} }

var skillDirs = map[string]string{
	"claude": ".claude/skills",
	"cursor": ".cursor/skills",
	"grok":   ".grok/skills",
	"agents": ".agents/skills",
}

func (a *app) skillCmd() *cobra.Command {
	skill := &cobra.Command{
		Use:   "skill",
		Short: "Print or install the agent skill bundled in this binary",
	}
	skill.AddCommand(&cobra.Command{
		Use:   "print [tgbot|tgbot-user]",
		Short: "Print a SKILL.md to stdout (default: tgbot, the bot skill)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _, err := pickSkill(args)
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(a.out, body)
			return err
		},
	})

	var agent, dir string
	var global bool
	install := &cobra.Command{
		Use:   "install [tgbot|tgbot-user]",
		Short: "Write a SKILL.md for an agent (project-local by default)",
		Long: `Install a bundled skill. "tgbot" (default) teaches the Bot API commands;
"tgbot-user" teaches 'tgbot user ...' (your own Telegram account).`,
		Example: `  tgbot skill install                       # ./.claude/skills/tgbot/SKILL.md
  tgbot skill install tgbot-user --global   # ~/.claude/skills/tgbot-user/SKILL.md
  tgbot skill install --agent cursor --global
  tgbot skill install --agent grok
  tgbot skill install --dir ./my/skills/tgbot`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, name, err := pickSkill(args)
			if err != nil {
				return err
			}
			target := dir
			if target == "" {
				sub, ok := skillDirs[agent]
				if !ok {
					return usageErr("--agent must be one of claude, cursor, grok, agents")
				}
				base, err := os.Getwd()
				if global {
					base, err = os.UserHomeDir()
				}
				if err != nil {
					return runtimeErr(err)
				}
				target = filepath.Join(base, sub, name)
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return runtimeErr(err)
			}
			file := filepath.Join(target, "SKILL.md")
			if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
				return runtimeErr(err)
			}
			fmt.Fprintln(a.out, file)
			return nil
		},
	}
	install.Flags().StringVar(&agent, "agent", "claude", "claude, cursor, grok or agents (the shared .agents/skills folder)")
	install.Flags().BoolVar(&global, "global", false, "install into the home directory instead of the current project")
	install.Flags().StringVar(&dir, "dir", "", "exact target directory (overrides --agent and --global)")
	skill.AddCommand(install)
	return skill
}

func pickSkill(args []string) (body, name string, err error) {
	name = "tgbot"
	if len(args) == 1 {
		name = args[0]
	}
	p, ok := skills[name]
	if !ok {
		return "", "", usageErr("unknown skill %q (available: %s)", name, strings.Join(skillNames(), ", "))
	}
	return *p, name, nil
}
