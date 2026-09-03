package cmd

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// versionInfo is the machine-readable record behind `gotty version --json`
// (name/version/commit/go_version/os/arch), for self-update tooling and
// support triage.
type versionInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func buildVersionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := versionInfo{
				Name:      "gotty",
				Version:   Version,
				Commit:    CommitID,
				GoVersion: runtime.Version(),
				OS:        runtime.GOOS,
				Arch:      runtime.GOARCH,
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				b, err := json.MarshalIndent(info, "", "  ")
				if err != nil {
					return err
				}
				_, err = fmt.Println(string(b))
				return err
			}
			_, err := fmt.Printf("gotty version %s (commit %s, %s %s/%s)\n",
				info.Version, info.Commit, info.GoVersion, info.OS, info.Arch)
			return err
		},
	}
	cmd.Flags().Bool("json", false, "Output as JSON (name/version/commit/go_version/os/arch)")
	return cmd
}
