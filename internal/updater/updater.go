// Package updater replaces the running binary with the latest GitHub release.
package updater

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
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
	"time"
)

// Repo is the GitHub repository releases are published to.
const Repo = "traptiterbeats/wpauto"

var client = &http.Client{Timeout: 5 * time.Minute}

// Latest returns the newest release tag.
func Latest() (string, error) {
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+Repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("github returned %s", resp.Status)
	}
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	return r.TagName, nil
}

// Update downloads the release archive for this architecture, verifies its checksum and
// atomically replaces the binary at target.
func Update(target string) (string, error) {
	tag, err := Latest()
	if err != nil {
		return "", err
	}
	asset := fmt.Sprintf("wpauto_linux_%s.tar.gz", runtime.GOARCH)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s/", Repo, tag)

	sums, err := fetch(base + "checksums.txt")
	if err != nil {
		return "", err
	}
	want := ""
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == asset {
			want = f[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("no checksum for %s in release %s", asset, tag)
	}
	archive, err := fetch(base + asset)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != want {
		return "", errors.New("checksum mismatch, update aborted")
	}
	bin, err := extract(archive, "wpauto")
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(filepath.Dir(target), ".wpauto.new")
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	return tag, nil
}

func fetch(url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func extract(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("%s not found in archive", name)
}
