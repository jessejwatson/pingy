package monitor

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestSettingsDefaultAndSave(t *testing.T) {
	home := t.TempDir()
	s, err := LoadSettings(home)
	if err != nil || s.HistoryMB != DefaultHistoryMB {
		t.Fatalf("no settings file: %+v, %v", s, err)
	}
	if err := SaveSettings(home, Settings{HistoryMB: 64}); err != nil {
		t.Fatal(err)
	}
	if s, _ := LoadSettings(home); s.HistoryBytes() != 64<<20 {
		t.Fatalf("saved: %+v", s)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int{"64": 64, "64M": 64, "512mb": 512, "1G": 1024, "1.5GB": 1536, "2GiB": 2048} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-5", "lots", "500K", "100G"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) worked", in)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{500: "500 B", 141628: "138 KB", 1 << 20: "1 MB", 3 << 19: "1.5 MB", 32 << 20: "32 MB", 3 << 29: "1.5 GB"} {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%d) = %q; want %q", n, got, want)
		}
	}
}

func TestTrimKeepsTheNewestWholeLines(t *testing.T) {
	path := t.TempDir() + "/samples"
	var b []byte
	t0 := time.UnixMilli(1_700_000_000_000)
	for i := range 1000 {
		b = append(b, formatSample(Sample{At: t0.Add(time.Duration(i) * time.Second), RTT: time.Millisecond})...)
	}
	os.WriteFile(path, b, 0o600)

	trim(path, int64(len(b))) // fits, so untouched
	if got, _ := os.ReadFile(path); !bytes.Equal(got, b) {
		t.Fatal("trimmed a file that fit")
	}
	limit := int64(len(b) / 2)
	trim(path, limit)
	got, _ := os.ReadFile(path)
	if int64(len(got)) > limit*3/4 || int64(len(got)) < limit*3/4-30 {
		t.Fatalf("trimmed to %d bytes; want about %d", len(got), limit*3/4)
	}
	if !bytes.HasSuffix(b, got) {
		t.Fatal("trim didn't keep the newest samples")
	}
	s, used := parseSamples(got, nil)
	if used != len(got) || !s[len(s)-1].At.Equal(t0.Add(999*time.Second)) {
		t.Fatalf("trimmed file doesn't start on a line: %d of %d bytes parsed", used, len(got))
	}
}
