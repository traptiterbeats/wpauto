// Package updater replaces the running binary with the latest GitHub release.
//
// The stable channel ("wpauto update") uses GitHub's "latest release", which never returns a
// pre-release, so stable users only ever get stable releases. The beta channel
// ("wpauto update --beta") takes the highest version among all published releases, including
// pre-releases such as v0.15.0-beta.1 (a newer stable release wins over an older beta).
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
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository releases are published to.
const Repo = "traptiterbeats/wpauto"

var client = &http.Client{Timeout: 15 * time.Minute}

// apiBase and downloadBase are variables so tests can point them at a fake server.
var (
	apiBase      = "https://api.github.com"
	downloadBase = "https://github.com"
)

// Progress, when set, receives step lines and a live download bar (percent, MB, speed).
var Progress io.Writer

func say(format string, a ...any) {
	if Progress != nil {
		fmt.Fprintf(Progress, format+"\n", a...)
	}
}

// Latest returns the newest stable release tag (GitHub's latest release; never a pre-release).
func Latest() (string, error) {
	req, _ := http.NewRequest("GET", apiBase+"/repos/"+Repo+"/releases/latest", nil)
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

// LatestBeta returns the highest version among the published releases, pre-releases included.
func LatestBeta() (string, error) {
	req, _ := http.NewRequest("GET", apiBase+"/repos/"+Repo+"/releases?per_page=50", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("github returned %s", resp.Status)
	}
	var list []struct {
		TagName string `json:"tag_name"`
		Draft   bool   `json:"draft"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", err
	}
	best := ""
	for _, r := range list {
		if r.Draft || r.TagName == "" {
			continue
		}
		if best == "" || CompareVersions(r.TagName, best) > 0 {
			best = r.TagName
		}
	}
	if best == "" {
		return "", errors.New("no published release found")
	}
	return best, nil
}

// CompareVersions compares two semantic versions ("v1.2.3", "1.2.3-beta.1"): -1, 0 or 1.
// A release sorts after its own pre-releases (1.2.3 > 1.2.3-rc.1 > 1.2.3-beta.2 > 1.2.3-beta.1).
func CompareVersions(a, b string) int {
	ac, ap, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	bc, bp, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	if c := compareIdents(strings.Split(ac, "."), strings.Split(bc, "."), true); c != 0 {
		return c
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	return compareIdents(strings.Split(ap, "."), strings.Split(bp, "."), false)
}

// compareIdents compares dot-separated identifiers: numbers numerically, words as text, and
// (for the core version) a missing part as 0; in a pre-release the longer list is newer.
func compareIdents(a, b []string, core bool) int {
	for i := 0; i < max(len(a), len(b)); i++ {
		if i >= len(a) || i >= len(b) {
			if core {
				x, y := "0", "0"
				if i < len(a) {
					x = a[i]
				}
				if i < len(b) {
					y = b[i]
				}
				if c := compareIdent(x, y); c != 0 {
					return c
				}
				continue
			}
			if i >= len(a) {
				return -1
			}
			return 1
		}
		if c := compareIdent(a[i], b[i]); c != 0 {
			return c
		}
	}
	return 0
}

func compareIdent(x, y string) int {
	xn, xerr := strconv.Atoi(x)
	yn, yerr := strconv.Atoi(y)
	switch {
	case xerr == nil && yerr == nil:
		return cmpInt(xn, yn)
	case xerr == nil:
		return -1 // numbers sort before words
	case yerr == nil:
		return 1
	}
	return strings.Compare(x, y)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Update installs the newest stable release (see UpdateChannel).
func Update(target string) (string, error) {
	return UpdateChannel(target, false)
}

// UpdateChannel downloads the release archive for this architecture — the newest stable release,
// or with beta the newest release including pre-releases — verifies its checksum and atomically
// replaces the binary at target.
func UpdateChannel(target string, beta bool) (string, error) {
	var tag string
	var err error
	if beta {
		tag, err = LatestBeta()
	} else {
		tag, err = Latest()
	}
	if err != nil {
		return "", err
	}
	if beta {
		say("  newest release (beta channel): %s", tag)
	} else {
		say("  latest release: %s", tag)
	}
	return install(target, tag)
}

func install(target, tag string) (string, error) {
	asset := fmt.Sprintf("wpauto_linux_%s.tar.gz", runtime.GOARCH)
	base := fmt.Sprintf("%s/%s/releases/download/%s/", downloadBase, Repo, tag)

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
	say("  downloading %s ...", asset)
	archive, err := fetchProgress(base+asset, Progress)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != want {
		return "", errors.New("checksum mismatch, update aborted")
	}
	say("  ✔ checksum verified")
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
	say("  ✔ installed %s (%.1f MB)", target, float64(len(bin))/1e6)
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

// stallTimeout aborts a download that receives nothing for this long.
var stallTimeout = 60 * time.Second

// fetchProgress downloads url, drawing a progress bar on w (when not nil) and failing
// if no data arrives for stallTimeout, so a stuck download never hangs silently.
func fetchProgress(url string, w io.Writer) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	total := resp.ContentLength
	var buf []byte
	chunk := make([]byte, 64<<10)
	start, last := time.Now(), time.Time{}
	type result struct {
		n   int
		err error
	}
	for {
		ch := make(chan result, 1)
		go func() { n, err := resp.Body.Read(chunk); ch <- result{n, err} }()
		var r result
		select {
		case r = <-ch:
		case <-time.After(stallTimeout):
			if w != nil {
				fmt.Fprintln(w)
			}
			return nil, fmt.Errorf("download stalled: no data for %s (got %.1f MB) — check the connection to github.com and try again", stallTimeout, float64(len(buf))/1e6)
		}
		buf = append(buf, chunk[:r.n]...)
		done := r.err == io.EOF
		if w != nil && (done || time.Since(last) > 250*time.Millisecond) {
			last = time.Now()
			fmt.Fprint(w, "\r"+progressLine(int64(len(buf)), total, time.Since(start)))
			if done {
				fmt.Fprintln(w)
			}
		}
		if done {
			return buf, nil
		}
		if r.err != nil {
			if w != nil {
				fmt.Fprintln(w)
			}
			return nil, r.err
		}
	}
}

func progressLine(got, total int64, elapsed time.Duration) string {
	speed := float64(got) / 1e6 / max(elapsed.Seconds(), 0.001)
	if total <= 0 {
		return fmt.Sprintf("  %.1f MB  %.2f MB/s   ", float64(got)/1e6, speed)
	}
	pct := int(got * 100 / total)
	bar := strings.Repeat("█", pct/5) + strings.Repeat("░", 20-pct/5)
	eta := ""
	if speed > 0 && got < total {
		eta = fmt.Sprintf("  ~%ds left", int(float64(total-got)/1e6/speed))
	}
	return fmt.Sprintf("  %s %3d%%  %.1f/%.1f MB  %.2f MB/s%s   ", bar, pct, float64(got)/1e6, float64(total)/1e6, speed, eta)
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
