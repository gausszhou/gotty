package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"
)

// DefaultBaseURL is the GitHub REST API root used when GOTTY_UPDATE_URL is
// unset.
const DefaultBaseURL = "https://api.github.com"

// Release is the subset of the GitHub release object (also served by
// self-hosted static mirrors of the same shape).
type Release struct {
	TagName string  `json:"tag_name"`
	Name    string  `json:"name"`
	Body    string  `json:"body"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Asset is one release asset (binary or sha256sums.txt).
type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// NewClient returns an HTTP client with a sane timeout and a Go user agent
// (GitHub rejects requests without one).
func NewClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}

// fetchRelease retrieves the release JSON from url into r, using client.
func fetchRelease(ctx context.Context, client *http.Client, url string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("fetch %s: %s %s", url, resp.Status, string(b))
	}
	var r Release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode release from %s: %w", url, err)
	}
	if r.TagName == "" {
		return nil, fmt.Errorf("release at %s has no tag_name", url)
	}
	return &r, nil
}

// LatestRelease returns the newest release of repo (owner/name). When
// overrideURL is non-empty (GOTTY_UPDATE_URL) it is fetched directly, in
// place of the GitHub API — for self-hosted static sites serving the same
// release JSON shape.
func LatestRelease(ctx context.Context, client *http.Client, overrideURL, repo string) (*Release, error) {
	if overrideURL != "" {
		return fetchRelease(ctx, client, overrideURL)
	}
	return fetchRelease(ctx, client, fmt.Sprintf("%s/repos/%s/releases/latest", DefaultBaseURL, repo))
}

// ReleaseForTag returns the release tagged version (e.g. "v2.1.0").
func ReleaseForTag(ctx context.Context, client *http.Client, overrideURL, repo, version string) (*Release, error) {
	if overrideURL != "" {
		// 自建站点无法枚举任意 tag,若索引 URL 未指向具体版本则原样返回,
		// 由调用方在版本比对时发现差异。
		return fetchRelease(ctx, client, overrideURL)
	}
	return fetchRelease(ctx, client, fmt.Sprintf("%s/repos/%s/releases/tags/%s", DefaultBaseURL, repo, version))
}

// FindAsset returns the release asset whose Name matches, or an error
// listing what was available (useful for debugging mirror setups).
func FindAsset(r *Release, name string) (*Asset, error) {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i], nil
		}
	}
	names := make([]string, 0, len(r.Assets))
	for _, a := range r.Assets {
		names = append(names, a.Name)
	}
	return nil, fmt.Errorf("asset %q not found in release %s (assets: %v)", name, r.TagName, names)
}

// AssetName returns the release asset name for this platform, mirroring
// the Makefile naming: gotty-{os}-{arch}[.exe].
func AssetName(goos, goarch string) string {
	name := fmt.Sprintf("gotty-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// PlatformAsset finds the binary asset matching the current platform.
func PlatformAsset(r *Release, goos, goarch string) (*Asset, error) {
	return FindAsset(r, AssetName(goos, goarch))
}

// Download fetches url into memory (binaries are a few MB; fine to hold).
func Download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// Env holds the runtime platform data for self-update; runtime defaults
// make it testable.
type Env struct {
	GOOS   string
	GOARCH string
	// Executable returns the path of the running gotty binary (or the
	// binary to replace in tests).
	Executable func() (string, error)
}

// DefaultEnv reflects the running process.
func DefaultEnv() Env {
	return Env{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Executable: execPath}
}

// osExecutable is indirection over os.Executable so tests can redirect it.
var osExecutable = os.Executable

func execPath() (string, error) {
	return osExecutable()
}
