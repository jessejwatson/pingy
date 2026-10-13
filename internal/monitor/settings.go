package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultHistoryMB is how much history each host keeps unless told
	// otherwise: about two weeks at one ping a second.
	DefaultHistoryMB = 32
	// MinHistoryMB and MaxHistoryMB bound the history setting.
	MinHistoryMB = 1
	MaxHistoryMB = 64 << 10
	// sampleBytes is about how much one ping takes in a samples file.
	sampleBytes = 20
)

// Settings is what pingy keeps in settings.json, next to hosts/. Every
// monitor reads it again when it changes.
type Settings struct {
	// HistoryMB caps each host's samples file, in MB (2^20 bytes); once
	// it's reached, the oldest quarter is dropped.
	HistoryMB int `json:"history_mb,omitempty"`
}

func settingsPath(home string) string { return filepath.Join(home, "settings.json") }

// LoadSettings reads home's settings; none saved yet gives the defaults.
func LoadSettings(home string) (Settings, error) {
	var s Settings
	b, err := os.ReadFile(settingsPath(home))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return s, err
	}
	if err == nil {
		if err := json.Unmarshal(b, &s); err != nil {
			return s, fmt.Errorf("can't read %s: %v", settingsPath(home), err)
		}
	}
	if s.HistoryMB == 0 {
		s.HistoryMB = DefaultHistoryMB
	}
	s.HistoryMB = min(max(s.HistoryMB, MinHistoryMB), MaxHistoryMB)
	return s, nil
}

// SaveSettings writes home's settings atomically.
func SaveSettings(home string, s Settings) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return writeJSON(settingsPath(home), s)
}

// HistoryBytes is the cap on each host's samples file.
func (s Settings) HistoryBytes() int64 { return int64(s.HistoryMB) << 20 }

// HistorySpan is about how far back a host's history reaches once full,
// pinging every every: between the cap less a quarter and the cap.
func (s Settings) HistorySpan(every time.Duration) (least, most time.Duration) {
	pings := s.HistoryBytes() / sampleBytes
	most = time.Duration(pings) * every
	return most * 3 / 4, most
}

// ParseSize reads a history size: MB by default, or with a unit, e.g.
// 64, 64M, 512MB or 1.5G.
func ParseSize(v string) (mb int, err error) {
	s := strings.ToUpper(strings.TrimSpace(v))
	s = strings.TrimSuffix(strings.TrimSuffix(s, "IB"), "B")
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "G"):
		mult, s = 1024, s[:len(s)-1]
	case strings.HasSuffix(s, "M"):
		s = s[:len(s)-1]
	}
	f, perr := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if perr != nil || f <= 0 {
		return 0, fmt.Errorf("can't read size %q; give MB, e.g. 64, or a size like 512M or 1G", v)
	}
	mb = int(f*mult + 0.5)
	if mb < MinHistoryMB || mb > MaxHistoryMB {
		return 0, fmt.Errorf("the history size must be from %d MB to %d GB", MinHistoryMB, MaxHistoryMB>>10)
	}
	return mb, nil
}

// FormatBytes shows n bytes the way pingy talks about history sizes:
// 812 KB, 1.5 MB, 32 MB, 2 GB.
func FormatBytes(n int64) string {
	unit := func(size float64, name string) string {
		f := 1
		if size >= 10 {
			f = 0
		}
		return strings.TrimSuffix(strconv.FormatFloat(size, 'f', f, 64), ".0") + " " + name
	}
	switch {
	case n >= 1<<30:
		return unit(float64(n)/(1<<30), "GB")
	case n >= 1<<20:
		return unit(float64(n)/(1<<20), "MB")
	case n >= 1<<10:
		return unit(float64(n)/(1<<10), "KB")
	}
	return strconv.FormatInt(n, 10) + " B"
}

// FormatMB shows a history setting, e.g. 32 MB or 1.5 GB.
func FormatMB(mb int) string { return FormatBytes(int64(mb) << 20) }
