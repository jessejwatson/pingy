package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// pingy looks, at most every few hours, for a newer version than its own,
// so the panel can say there's one and how to get it. Through Homebrew,
// newer means in the tap's formula, as that's what brew upgrade installs;
// otherwise it means a newer tag on GitHub.
const (
	tapFormulaURL = "https://raw.githubusercontent.com/jessejwatson/homebrew-tap/HEAD/Formula/pingy.rb"
	tagsURL       = "https://api.github.com/repos/jessejwatson/pingy/tags?per_page=100"
	updateEvery   = 3 * time.Hour
)

// updateFile is what the last look found, kept between runs.
type updateFile struct {
	Checked time.Time `json:"checked"`
	Source  string    `json:"source"` // the URL looked at
	Latest  string    `json:"latest"` // e.g. "0.1.9"; "" when none was found
}

func updatePath(home string) string { return filepath.Join(home, "update.json") }

var versionRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// parseVersion reads a release's version, e.g. "0.1.9" or "v0.1.9"; a
// build between releases, e.g. "0.1.9-2-gabc1234", or "dev", isn't one.
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	m := versionRe.FindStringSubmatch(s)
	if m == nil || m[0] != s {
		return v, false
	}
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

func newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// viaBrew says whether this pingy was installed by Homebrew.
func viaBrew() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return strings.Contains(exe, "/Cellar/pingy/")
}

// upgradeCommand is how to get the newer version.
func upgradeCommand(brew bool) string {
	if brew {
		return "brew upgrade pingy"
	}
	return "git pull && make install"
}

// updateCheckWanted says whether to look for a newer version at all: not
// for a build between releases, nor with PINGY_NO_UPDATE_CHECK set.
func (a *App) updateCheckWanted() bool {
	_, ok := parseVersion(a.Version)
	return ok && os.Getenv("PINGY_NO_UPDATE_CHECK") == "" && a.Home != ""
}

// availableUpdate is a newer version than this one, if the last look
// found one, and whether it's time to look again.
func (a *App) availableUpdate(source string) (latest string, stale bool) {
	var f updateFile
	if b, err := os.ReadFile(updatePath(a.Home)); err == nil {
		json.Unmarshal(b, &f)
	}
	stale = f.Source != source || time.Since(f.Checked) > updateEvery || f.Checked.After(time.Now())
	if f.Source != source {
		return "", stale
	}
	have, _ := parseVersion(a.Version)
	if v, ok := parseVersion(f.Latest); ok && newer(v, have) {
		return f.Latest, stale
	}
	return "", stale
}

// checkUpdate looks for a newer version, from the last look if it was
// recent enough, and says which it is ("" for none).
func (a *App) checkUpdate(brew bool) string {
	source := tagsURL
	if brew {
		source = tapFormulaURL
	}
	latest, stale := a.availableUpdate(source)
	if !stale {
		return latest
	}
	found, err := fetchLatest(source)
	if err != nil {
		return latest // try again next time
	}
	if os.MkdirAll(a.Home, 0o700) == nil {
		writeFileAtomic(updatePath(a.Home), updateFile{Checked: time.Now(), Source: source, Latest: found})
	}
	latest, _ = a.availableUpdate(source)
	return latest
}

// fetchLatest reads the newest version at source.
func fetchLatest(source string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", source, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "pingy")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &httpError{resp.Status}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return latestIn(source, b), nil
}

type httpError struct{ status string }

func (e *httpError) Error() string { return e.status }

var tagURLRe = regexp.MustCompile(`refs/tags/(v\d+\.\d+\.\d+)\.tar\.gz`)

// latestIn finds the newest version in what source sent: the formula's
// url, or the highest of the tags.
func latestIn(source string, b []byte) string {
	var names []string
	if source == tapFormulaURL {
		if m := tagURLRe.FindSubmatch(b); m != nil {
			names = append(names, string(m[1]))
		}
	} else {
		var tags []struct {
			Name string `json:"name"`
		}
		json.Unmarshal(b, &tags)
		for _, t := range tags {
			names = append(names, t.Name)
		}
	}
	best, bestV := "", [3]int{}
	for _, n := range names {
		if v, ok := parseVersion(n); ok && (best == "" || newer(v, bestV)) {
			best, bestV = strings.TrimPrefix(n, "v"), v
		}
	}
	return best
}

func writeFileAtomic(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
