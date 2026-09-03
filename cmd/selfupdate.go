package cmd

import (
	"errors"
	"os"

	"github.com/gausszhou/gotty/internal/update"
	"github.com/spf13/cobra"
)

func buildSelfUpdateCmd() *cobra.Command {
	var (
		repo, version string
		yes, dryRun   bool
		check         bool
	)
	cmd := &cobra.Command{
		Use:   "self update",
		Short: "Update gotty to the latest release",
		Long: `Update the gotty binary to the newest release (default: latest, from
the GitHub releases API; GOTTY_UPDATE_URL overrides it with a release-JSON
index URL for self-hosted mirrors).

The new binary is downloaded, verified against the sha256sums.txt of the
same release, and atomically swapped into the current executable's
directory. Running processes are NOT restarted — sessions are the product
here, so the change takes effect on the next start (restart the service).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			baseURL := os.Getenv("GOTTY_UPDATE_URL")
			if repo == "" && baseURL == "" {
				repo = update.DefaultRepo
			}
			_, err := update.Run(cmd.Context(), update.NewClient(), update.Options{
				Repo:    repo,
				Version: version,
				BaseURL: baseURL,
				Current: Version,
				Yes:     yes,
				DryRun:  dryRun,
				Check:   check,
				Out:     cmd.OutOrStdout(),
			}, update.DefaultEnv())
			// --check 发现新版本:Run 已打印版本差,这里以退出码 1 表达
			// (main 会把 ErrOutdated 打印到 stderr 并 exit 1)。
			if errors.Is(err, update.ErrOutdated) {
				return err
			}
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&repo, "repo", "", "GitHub repository owner/name (default: "+update.DefaultRepo+")")
	flags.StringVar(&version, "version", "", "Target version tag, e.g. v2.1.0 (default: latest release)")
	flags.BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	flags.BoolVar(&dryRun, "dry-run", false, "Query and report only — never download or replace")
	flags.BoolVar(&check, "check", false, "Report the version difference only; exit 1 when a newer version exists")
	return cmd
}
