// Package update implements `gotty self update`: it queries a release
// index (GitHub releases/latest by default, or a GOTTY_UPDATE_URL static
// mirror), compares versions, downloads the platform binary plus its
// sha256 checksums, verifies the digest and atomically replaces the
// running executable. The pieces are unit-testable without network:
// semver comparison, checksum parsing/verification and the atomic replace
// are pure functions.
package update

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// ErrOutdated signals `--check` found a newer release; callers map it to a
// non-zero exit code.
var ErrOutdated = errors.New("a newer version is available")

// DefaultRepo is the upstream binary source.
const DefaultRepo = "gausszhou/gotty"

// Options configures one self-update run.
type Options struct {
	Repo    string // owner/name, ignored when BaseURL is set
	Version string // explicit target tag (e.g. "v2.1.0"); empty = latest
	BaseURL string // GOTTY_UPDATE_URL: release JSON index URL; empty = GitHub API
	Current string // local version (cmd.Version from ldflags)
	Yes     bool   // skip the confirmation prompt
	DryRun  bool   // query + report only, never download/replace
	Check   bool   // only report the version difference (exit 1 when stale)
	Out     io.Writer
}

// Outcome classifies the result of a run.
type Outcome int

const (
	OutcomeUpToDate Outcome = iota
	OutcomeUpdated
	OutcomeDryRun
	OutcomeCheckedOutdated
)

// Result carries what the command layer needs to report.
type Result struct {
	Outcome        Outcome
	CurrentVersion string
	TargetVersion  string
	Changelog      string
	AssetName      string
	AssetSize      int64
	DownloadURL    string
	TargetPath     string
}

// Run executes the self-update flow end to end.
func Run(ctx context.Context, client *http.Client, opts Options, env Env) (Result, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	// 本地版本可能不是 semver(git describe 无 tag 时的 hash、开发构建):
	// 无法比较即视为落后于任何发布版,但如实提示。
	current := opts.Current
	parseErr := error(nil)
	var cmp int
	if _, err := ParseVersion(current); err != nil {
		parseErr = err
		current = "0.0.0-dev"
	}

	var rel *Release
	var err error
	if opts.Version != "" {
		rel, err = ReleaseForTag(ctx, client, opts.BaseURL, opts.Repo, opts.Version)
	} else {
		rel, err = LatestRelease(ctx, client, opts.BaseURL, opts.Repo)
	}
	if err != nil {
		return Result{}, err
	}

	target := rel.TagName
	if parseErr == nil {
		cmp, err = CompareVersions(target, current)
		if err != nil {
			return Result{}, fmt.Errorf("compare versions %q vs %q: %w", target, current, err)
		}
	} else {
		cmp = 1 // 非 semver 本地版:直接视为落后
	}

	if cmp <= 0 {
		fmt.Fprintf(out, "gotty is already up to date (%s).\n", current)
		return Result{Outcome: OutcomeUpToDate, CurrentVersion: current, TargetVersion: target}, nil
	}
	if parseErr != nil {
		fmt.Fprintf(out, "note: current version %q is not a semantic version (%v); treating it as outdated.\n",
			opts.Current, parseErr)
	}

	// 展示变更与差异。
	if rel.Body != "" {
		fmt.Fprintf(out, "\nChanges in %s:\n%s\n", target, strings.TrimSpace(rel.Body))
	}
	fmt.Fprintf(out, "New version available: %s → %s.\n", current, target)

	if opts.Check {
		fmt.Fprintf(out, "run `gotty self update` to upgrade.\n")
		return Result{
			Outcome: OutcomeCheckedOutdated, CurrentVersion: current, TargetVersion: target,
		}, ErrOutdated
	}

	asset, err := PlatformAsset(rel, env.GOOS, env.GOARCH)
	if err != nil {
		return Result{}, err
	}
	exe, err := env.Executable()
	if err != nil {
		return Result{}, fmt.Errorf("locate current binary: %w", err)
	}

	// dry-run:查资产信息并报告,不下载不替换、也不用确认。
	if opts.DryRun {
		fmt.Fprintf(out, "would download %s (%d bytes) from %s and replace %s.\n",
			asset.Name, asset.Size, asset.BrowserDownloadURL, exe)
		return Result{
			Outcome: OutcomeDryRun, CurrentVersion: current, TargetVersion: target,
			Changelog: rel.Body, AssetName: asset.Name, AssetSize: asset.Size,
			DownloadURL: asset.BrowserDownloadURL, TargetPath: exe,
		}, nil
	}

	if !opts.Yes {
		ok, perr := confirm(out, fmt.Sprintf("Update gotty %s → %s? [y/N] ", current, target))
		if perr != nil {
			return Result{}, perr
		}
		if !ok {
			fmt.Fprintln(out, "aborted; the current binary was left untouched.")
			return Result{Outcome: OutcomeUpToDate, CurrentVersion: current, TargetVersion: target}, nil
		}
	}

	checksumAsset, err := FindAsset(rel, "sha256sums.txt")
	if err != nil {
		return Result{}, fmt.Errorf("release %s has no sha256sums.txt asset: %w", target, err)
	}

	bin, err := Download(ctx, client, asset.BrowserDownloadURL)
	if err != nil {
		return Result{}, err
	}
	sumsData, err := Download(ctx, client, checksumAsset.BrowserDownloadURL)
	if err != nil {
		return Result{}, err
	}
	sums, err := ParseChecksums(sumsData)
	if err != nil {
		return Result{}, err
	}
	want, err := sums.Lookup(asset.Name)
	if err != nil {
		return Result{}, err
	}
	if err := VerifyChecksum(bin, want); err != nil {
		return Result{}, fmt.Errorf("%s: downloaded binary failed verification; refusing to install: %w",
			asset.Name, err)
	}

	if err := AtomicReplace(exe, bin); err != nil {
		msg := "the update was verified but could not be installed — run the install script " +
			"(curl -fsSL https://raw.githubusercontent.com/gausszhou/gotty/master/scripts/install.sh | sh) " +
			"or write to " + exe + " yourself: " + err.Error()
		return Result{}, errors.New(msg)
	}

	fmt.Fprintf(out, "updated to %s at %s — restart the service (systemd/systemctl --user restart gotty) to take effect.\n",
		target, exe)
	return Result{
		Outcome: OutcomeUpdated, CurrentVersion: current, TargetVersion: target,
		Changelog: rel.Body, AssetName: asset.Name, AssetSize: asset.Size,
		DownloadURL: asset.BrowserDownloadURL, TargetPath: exe,
	}, nil
}

// confirm asks a y/N question on out and reads the answer from stdin.
func confirm(out io.Writer, prompt string) (bool, error) {
	fmt.Fprint(out, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
