// Package update checks GitHub releases for a newer Banyan and installs
// it: a setup on Windows, a zip of the app bundle on macOS, and a tarball
// on Linux.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Zenyet/banyan/internal/i18n"
)

// ManifestURL is the update manifest of the latest release. It is a
// variable so that tests can point it at another server.
var ManifestURL = "https://github.com/Zenyet/banyan/releases/latest/download/latest.json"

// Client fetches the manifest.
var Client = &http.Client{Timeout: 30 * time.Second}

// DownloadClient fetches a release asset, which can be tens of megabytes.
var DownloadClient = &http.Client{Timeout: 5 * time.Minute}

// Asset is one platform's download, checked before it is run.
type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is latest.json in a release.
type Manifest struct {
	Version   string           `json:"version"`
	NotesURL  string           `json:"notes_url"`
	Platforms map[string]Asset `json:"platforms"`
}

// Platform is the key of this build in a manifest, such as darwin/arm64.
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Asset returns the download for this build.
func (m *Manifest) Asset() (Asset, bool) {
	a, ok := m.Platforms[Platform()]
	return a, ok
}

// Check returns the latest release when it is newer than current. A
// development build, and a release that is not newer, return nil.
func Check(ctx context.Context, current string) (*Manifest, error) {
	if current == "" || current == "dev" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ManifestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Banyan/"+current)
	resp, err := Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New(i18n.T("The latest release has no update manifest; download it from the release page"))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s (HTTP %d)", i18n.T("The update server did not answer"), resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("The update manifest could not be read"), err)
	}
	if !newer(m.Version, current) {
		return nil, nil
	}
	return &m, nil
}

// newer reports whether release is a later x.y.z than current.
func newer(release, current string) bool {
	r, ok := versionParts(release)
	if !ok {
		return false
	}
	c, ok := versionParts(current)
	if !ok {
		return false
	}
	for i := range r {
		if r[i] != c[i] {
			return r[i] > c[i]
		}
	}
	return false
}

func versionParts(version string) ([3]int, bool) {
	var out [3]int
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if i := strings.IndexAny(version, "-+"); i >= 0 {
		version = version[:i]
	}
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return out, false
	}
	for i, part := range parts {
		n := 0
		if part == "" {
			return out, false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return out, false
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out, true
}

// Install downloads the release's asset for this build, checks it, and
// starts the update. The app must quit after this returns nil, as the
// updater replaces its files and restarts it.
func Install(ctx context.Context, m *Manifest) error {
	a, ok := m.Asset()
	if !ok {
		return fmt.Errorf("%s %s", i18n.T("There is no update for this system:"), Platform())
	}
	dir, err := os.MkdirTemp("", "banyan-update-")
	if err != nil {
		return err
	}
	name := filepath.Base(pathFromURL(a.URL))
	if name == "." || name == "/" || name == "" {
		name = "banyan-update"
	}
	file := filepath.Join(dir, name)
	if err := download(ctx, a, file); err != nil {
		os.RemoveAll(dir)
		return err
	}
	if err := installAsset(file, dir, m.Version); err != nil {
		os.RemoveAll(dir)
		return err
	}
	return nil
}

func pathFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return path.Base(u.Path)
}

// download fetches the asset and checks its size and SHA-256.
func download(ctx context.Context, a Asset, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Banyan")
	resp, err := DownloadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s (HTTP %d)", i18n.T("The update could not be downloaded"), resp.StatusCode)
	}
	const maxSize = 300 << 20
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	written, err := io.Copy(f, io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return err
	}
	if written > maxSize {
		return errors.New(i18n.T("The update is larger than expected"))
	}
	if a.Size > 0 && written != a.Size {
		return fmt.Errorf("%s: %d, %s %d", i18n.T("The update has the wrong size"), written, i18n.T("expected"), a.Size)
	}
	if err := f.Close(); err != nil {
		return err
	}
	sum, err := fileSHA256(dest)
	if err != nil {
		return err
	}
	if a.SHA256 == "" || !strings.EqualFold(sum, a.SHA256) {
		return errors.New(i18n.T("The update's checksum does not match"))
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
