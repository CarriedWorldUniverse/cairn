// Package selfupdate updates the cairn binary in place from GitHub releases.
// It queries the latest release, compares it to the running build's version,
// downloads the matching GoReleaser asset, verifies it against checksums.txt
// (SHA-256), and atomically replaces the current executable. No state is kept:
// re-running after a failed install is always safe.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/version"
)

// maxAssetSize caps release downloads. Real assets are ~10 MiB; the cap only
// guards against a hostile or misconfigured server streaming forever.
const maxAssetSize = 200 << 20

// Client talks to a GitHub-style releases API. The zero value is not usable;
// construct with New.
type Client struct {
	// BaseURL is the API root, e.g. https://api.github.com. Tests point this
	// at an httptest server.
	BaseURL string
	// Repo is the "owner/name" slug whose releases are consulted.
	Repo string
	// Token, when non-empty, is sent as a bearer credential (avoids the low
	// anonymous rate limit; the repo itself is public).
	Token string
	// HTTP is the transport; defaults to http.DefaultClient in New.
	HTTP *http.Client
	// OS/Arch select the release asset; default to the running platform in New.
	OS, Arch string
}

// New returns a Client for the canonical cairn repo, targeting the running
// platform. token may be empty.
func New(token string) *Client {
	return &Client{
		BaseURL: "https://api.github.com",
		Repo:    "CarriedWorldUniverse/cairn",
		Token:   token,
		HTTP:    http.DefaultClient,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
}

// Release is the slice of a GitHub release the updater needs.
type Release struct {
	Tag     string            // e.g. "v0.1.19"
	Version string            // Tag without the "v" prefix
	Assets  map[string]string // asset name -> download URL
}

// Result reports what Update decided and did.
type Result struct {
	Current string // the running build's version ("dev" for source builds)
	Latest  string // the latest released version
	Target  string // the executable path that was (or would be) replaced
	Newer   bool   // true when the latest release is newer than the running build (or the build is unversioned)
	Updated bool   // true when a new binary was installed
}

// ErrDevBuild is returned when the running binary is a source build ("dev"):
// there is no version to compare, so updating would be a blind overwrite.
var ErrDevBuild = errors.New(`this is a source build ("dev"), not a release — re-run with --force to replace it with the latest release`)

// Latest fetches the newest release and its asset URLs.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/repos/"+c.Repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("query latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("query latest release: %s returned %s", c.Repo, resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("decode latest release: %w", err)
	}
	if body.TagName == "" {
		return Release{}, fmt.Errorf("latest release of %s has no tag", c.Repo)
	}
	rel := Release{
		Tag:     body.TagName,
		Version: strings.TrimPrefix(body.TagName, "v"),
		Assets:  make(map[string]string, len(body.Assets)),
	}
	for _, a := range body.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// AssetName returns the GoReleaser archive name for a version + platform
// (mirrors name_template in .goreleaser.yaml).
func AssetName(ver, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("cairn_%s_%s_%s.%s", ver, goos, goarch, ext)
}

// Update checks the latest release against current and, unless checkOnly is
// set, downloads + verifies + installs it over targetPath. force installs the
// latest release even when current is "dev" or not older.
func (c *Client) Update(ctx context.Context, current, targetPath string, checkOnly, force bool) (Result, error) {
	rel, err := c.Latest(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{Current: current, Latest: rel.Version, Target: targetPath}
	cur, curErr := version.Parse(current)
	if latest, err := version.Parse(rel.Version); err != nil {
		return res, fmt.Errorf("latest release tag %q is not semver: %w", rel.Tag, err)
	} else if curErr != nil {
		res.Newer = true // unversioned build: can't compare, so a release always counts as available
	} else {
		res.Newer = version.Compare(latest, cur) > 0
	}
	if checkOnly {
		return res, nil
	}
	if !force {
		if curErr != nil {
			return res, ErrDevBuild
		}
		if !res.Newer {
			return res, nil // already up to date
		}
	}

	name := AssetName(rel.Version, c.OS, c.Arch)
	url, ok := rel.Assets[name]
	if !ok {
		return res, fmt.Errorf("release %s has no asset %s for this platform", rel.Tag, name)
	}
	sumsURL, ok := rel.Assets["checksums.txt"]
	if !ok {
		return res, fmt.Errorf("release %s has no checksums.txt", rel.Tag)
	}
	archive, err := c.download(ctx, url)
	if err != nil {
		return res, fmt.Errorf("download %s: %w", name, err)
	}
	sums, err := c.download(ctx, sumsURL)
	if err != nil {
		return res, fmt.Errorf("download checksums.txt: %w", err)
	}
	if err := verifyChecksum(sums, name, archive); err != nil {
		return res, err
	}
	bin, err := extractBinary(name, archive)
	if err != nil {
		return res, err
	}
	if err := install(bin, targetPath); err != nil {
		return res, err
	}
	res.Updated = true
	return res, nil
}

func (c *Client) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAssetSize {
		return nil, fmt.Errorf("%s exceeds the %d-byte asset cap", url, maxAssetSize)
	}
	return data, nil
}

// verifyChecksum checks data against the "<sha256>  <name>" line in a
// GoReleaser checksums.txt.
func verifyChecksum(sums []byte, name string, data []byte) error {
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt has no entry for %s", name)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return nil
}

// extractBinary pulls the cairn executable out of a release archive
// (tar.gz on linux/darwin, zip on windows).
func extractBinary(assetName string, archive []byte) ([]byte, error) {
	binName := "cairn"
	if strings.HasSuffix(assetName, ".zip") {
		binName = "cairn.exe"
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", assetName, err)
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) != binName {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return readCapped(rc)
		}
		return nil, fmt.Errorf("%s contains no %s", assetName, binName)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", assetName, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s contains no %s", assetName, binName)
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", assetName, err)
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == binName {
			return readCapped(tr)
		}
	}
}

func readCapped(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxAssetSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAssetSize {
		return nil, fmt.Errorf("archived binary exceeds the %d-byte cap", maxAssetSize)
	}
	return data, nil
}

// install atomically replaces target with bin: the new binary is written to a
// temp file in target's directory (same filesystem, so the rename is atomic),
// given target's mode, and renamed into place. On Windows a running executable
// cannot be overwritten, so the old binary is first renamed aside to
// target+".old" (left behind; harmless, replaced by the next update).
func install(bin []byte, target string) error {
	dir := filepath.Dir(target)
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".cairn-update-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot write to %s — re-run with elevated privileges (e.g. sudo cairn update): %w", dir, err)
		}
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := target + ".old"
		os.Remove(old)
		if err := os.Rename(target, old); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("move current binary aside: %w", err)
		}
	}
	if err := os.Rename(tmpName, target); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot replace %s — re-run with elevated privileges (e.g. sudo cairn update): %w", target, err)
		}
		return err
	}
	return nil
}
