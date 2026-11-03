package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	gottytuitest "github.com/gausszhou/gotty/skills/gotty-tui-test"
)

// buildSkillCmd creates `gotty skill`.
//
// The skill is embedded in the binary (see skills/gotty-tui-test), so the text
// it prints can never drift from the flags this binary actually accepts. It
// deliberately does not guess where an agent keeps its skills (`~/.claude`,
// `~/.agents`, …): `--install DIR` writes exactly where the caller says, and
// the docs show the two common targets.
func buildSkillCmd() *cobra.Command {
	var (
		installDir string
		force      bool
	)
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Print or install the gotty-tui-test agent skill",
		Long: "Print the gotty-tui-test skill (Markdown with YAML front matter), or\n" +
			"write it to a directory with --install.\n\n" +
			"Install it where your agent looks for skills, e.g.:\n\n" +
			"  gotty skill --install ~/.agents/skills/gotty-tui-test\n" +
			"  gotty skill --install ~/.claude/skills/gotty-tui-test\n\n" +
			"The text is embedded in this binary, so reinstalling after an upgrade\n" +
			"refreshes it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if installDir == "" {
				_, err := fmt.Fprint(cmd.OutOrStdout(), gottytuitest.Content)
				return err
			}
			path, err := installSkill(installDir, force)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&installDir, "install", "", "write SKILL.md into this directory (created if missing)")
	flags.BoolVar(&force, "force", false, "overwrite an existing SKILL.md")
	return cmd
}

// installSkill writes the embedded skill as <dir>/SKILL.md.
func installSkill(dir string, force bool) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if !force {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("%s already exists (use --force to overwrite)", path)
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("cannot check %s: %w", path, err)
		}
	}
	if err := os.WriteFile(path, []byte(gottytuitest.Content), 0o644); err != nil {
		return "", fmt.Errorf("cannot write %s: %w", path, err)
	}
	return path, nil
}
