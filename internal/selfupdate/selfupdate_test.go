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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeRelease serves a GitHub-shaped /releases/latest plus its assets and
// returns a Client pointed at it.
func fakeRelease(t *testing.T, tag string, binary []byte, goos, goarch string) *Client {
	t.Helper()
	ver := tag[1:] // strip "v"
	assetName := AssetName(ver, goos, goarch)

	var archive bytes.Buffer
	if goos == "windows" {
		zw := zip.NewWriter(&archive)
		w, err := zw.Create("cairn.exe")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(binary); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&archive)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: "cairn", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(binary); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256(archive.Bytes())
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/owner/cairn/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag,
			"assets": []map[string]string{
				{"name": assetName, "browser_download_url": srv.URL + "/dl/" + assetName},
				{"name": "checksums.txt", "browser_download_url": srv.URL + "/dl/checksums.txt"},
			},
		})
	})
	mux.HandleFunc("/dl/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive.Bytes())
	})
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(checksums))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &Client{BaseURL: srv.URL, Repo: "owner/cairn", HTTP: srv.Client(), OS: goos, Arch: goarch}
}

func TestUpdateInstallsNewerRelease(t *testing.T) {
	newBin := []byte("#!/new-cairn-binary")
	c := fakeRelease(t, "v0.2.0", newBin, "linux", "amd64")

	target := filepath.Join(t.TempDir(), "cairn")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil { // WriteFile perms are umask-masked
		t.Fatal(err)
	}
	res, err := c.Update(context.Background(), "0.1.0", target, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || res.Latest != "0.2.0" {
		t.Fatalf("result = %+v, want Updated=true Latest=0.2.0", res)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newBin) {
		t.Fatalf("installed binary = %q, want %q", got, newBin)
	}
	fi, _ := os.Stat(target)
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %v, want 0755", fi.Mode().Perm())
	}
}

func TestUpdateAlreadyCurrent(t *testing.T) {
	c := fakeRelease(t, "v0.2.0", []byte("bin"), "linux", "amd64")
	target := filepath.Join(t.TempDir(), "cairn")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cur := range []string{"0.2.0", "0.3.0"} {
		res, err := c.Update(context.Background(), cur, target, false, false)
		if err != nil {
			t.Fatalf("current=%s: %v", cur, err)
		}
		if res.Updated {
			t.Fatalf("current=%s: updated, want no-op", cur)
		}
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("binary was touched on a no-op update")
	}
}

func TestUpdateCheckOnly(t *testing.T) {
	c := fakeRelease(t, "v0.2.0", []byte("bin"), "linux", "amd64")
	target := filepath.Join(t.TempDir(), "cairn")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := c.Update(context.Background(), "0.1.0", target, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated {
		t.Fatal("check-only run installed")
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatal("check-only run touched the binary")
	}
}

func TestUpdateDevBuildRefusedUnlessForced(t *testing.T) {
	newBin := []byte("new")
	c := fakeRelease(t, "v0.2.0", newBin, "linux", "amd64")
	target := filepath.Join(t.TempDir(), "cairn")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Update(context.Background(), "dev", target, false, false); !errors.Is(err, ErrDevBuild) {
		t.Fatalf("dev update err = %v, want ErrDevBuild", err)
	}
	res, err := c.Update(context.Background(), "dev", target, true, false)
	if err != nil {
		t.Fatalf("dev --check err = %v, want nil (check must always work)", err)
	}
	if !res.Newer || res.Updated {
		t.Fatalf("dev --check result = %+v, want Newer=true Updated=false", res)
	}
	res, err = c.Update(context.Background(), "dev", target, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated {
		t.Fatal("forced dev update did not install")
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, newBin) {
		t.Fatal("forced dev update wrote wrong content")
	}
}

func TestUpdateChecksumMismatch(t *testing.T) {
	c := fakeRelease(t, "v0.2.0", []byte("bin"), "linux", "amd64")
	// Re-point checksums at garbage by wrapping the transport.
	base := c.HTTP.Transport
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(r)
		if err == nil && filepath.Base(r.URL.Path) == "checksums.txt" {
			resp.Body = readCloser("deadbeef  " + AssetName("0.2.0", "linux", "amd64") + "\n")
		}
		return resp, err
	})}
	target := filepath.Join(t.TempDir(), "cairn")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := c.Update(context.Background(), "0.1.0", target, false, false)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("checksum mismatch")) {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatal("binary replaced despite checksum mismatch")
	}
}

func TestUpdateWindowsZip(t *testing.T) {
	newBin := []byte("MZ-new")
	c := fakeRelease(t, "v0.2.0", newBin, "windows", "amd64")
	target := filepath.Join(t.TempDir(), "cairn.exe")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := c.Update(context.Background(), "0.1.0", target, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated {
		t.Fatal("zip update did not install")
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, newBin) {
		t.Fatal("zip update wrote wrong content")
	}
}

func TestAssetName(t *testing.T) {
	cases := []struct{ goos, goarch, want string }{
		{"linux", "amd64", "cairn_0.1.19_linux_amd64.tar.gz"},
		{"darwin", "arm64", "cairn_0.1.19_darwin_arm64.tar.gz"},
		{"windows", "amd64", "cairn_0.1.19_windows_amd64.zip"},
	}
	for _, c := range cases {
		if got := AssetName("0.1.19", c.goos, c.goarch); got != c.want {
			t.Errorf("AssetName(%s/%s) = %s, want %s", c.goos, c.goarch, got, c.want)
		}
	}
}

func TestLatestSendsToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.1.0"})
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Repo: "owner/cairn", Token: "tok123", HTTP: srv.Client(), OS: "linux", Arch: "amd64"}
	if _, err := c.Latest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want Bearer tok123", gotAuth)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func readCloser(s string) *bodyCloser { return &bodyCloser{Reader: *bytes.NewBufferString(s)} }

type bodyCloser struct{ Reader bytes.Buffer }

func (b *bodyCloser) Read(p []byte) (int, error) { return b.Reader.Read(p) }
func (b *bodyCloser) Close() error               { return nil }
