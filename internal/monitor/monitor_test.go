package monitor

import (
	"os"
	"testing"
	"time"
)

func TestHistoryReadsOnlyWhatsNew(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/samples"
	t0 := time.UnixMilli(1_700_000_000_000)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(formatSample(Sample{At: t0, RTT: 12500 * time.Microsecond}))
	f.Write(formatSample(Sample{At: t0.Add(time.Second), RTT: -1}))
	f.Write([]byte("1700000002000 9")) // partly written

	h := NewHistory(path, 0)
	h.Refresh()
	if len(h.Samples) != 2 || h.Samples[0].Ms() != 12.5 || !h.Samples[1].Lost() {
		t.Fatalf("samples = %+v", h.Samples)
	}
	f.Write([]byte("000\n"))
	h.Refresh()
	if len(h.Samples) != 3 || h.Samples[2].RTT != 9*time.Millisecond {
		t.Fatalf("after append: %+v", h.Samples)
	}
	f.Close()

	// A trimmed file is read again from the start.
	os.WriteFile(path, formatSample(Sample{At: t0.Add(5 * time.Second), RTT: time.Millisecond}), 0o600)
	h.Refresh()
	if len(h.Samples) != 1 || !h.Samples[0].At.Equal(t0.Add(5*time.Second)) {
		t.Fatalf("after trim: %+v", h.Samples)
	}
}

func TestHistoryTailStartsOnALine(t *testing.T) {
	path := t.TempDir() + "/samples"
	var b []byte
	t0 := time.UnixMilli(1_700_000_000_000)
	for i := range 100 {
		b = append(b, formatSample(Sample{At: t0.Add(time.Duration(i) * time.Second), RTT: time.Millisecond})...)
	}
	os.WriteFile(path, b, 0o600)
	h := NewHistory(path, 50)
	h.Refresh()
	if len(h.Samples) == 0 || len(h.Samples) > 3 || !h.Samples[len(h.Samples)-1].At.Equal(t0.Add(99*time.Second)) {
		t.Fatalf("tail = %+v", h.Samples)
	}
	h.LoadAll()
	h.Refresh()
	if len(h.Samples) != 100 {
		t.Fatalf("full = %d samples", len(h.Samples))
	}
}

func TestSummarise(t *testing.T) {
	ms := func(v float64) Sample { return Sample{RTT: time.Duration(v * float64(time.Millisecond))} }
	st := Summarise([]Sample{ms(10), ms(20), {RTT: -1}, ms(30)})
	if st.Sent != 4 || st.Lost != 1 || st.Min != 10 || st.Max != 30 || st.Avg != 20 || st.Jitter != 10 || st.LossPct() != 25 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestCreateFindAndNames(t *testing.T) {
	root := t.TempDir()
	a, err := Create(root, "1.1.1.1", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Create(root, "example.com", "", 2*time.Second)
	if a.ID != "1" || b.ID != "2" {
		t.Fatalf("ids %s %s", a.ID, b.ID)
	}
	b.Name = "web"
	b.Save()
	if m := Find(root, "web"); m == nil || m.ID != "2" {
		t.Fatalf("find by name: %v", m)
	}
	if m := Find(root, "EXAMPLE.com"); m == nil || m.ID != "2" {
		t.Fatalf("find by target: %v", m)
	}
	if m := Find(root, "1"); m == nil || m.Target != "1.1.1.1" {
		t.Fatalf("find by id: %v", m)
	}
	if err := CheckName(root, "web", "1"); err == nil {
		t.Fatal("a taken name was allowed")
	}
	if err := CheckName(root, "1.1.1.1", "2"); err == nil {
		t.Fatal("another host's address was allowed as a name")
	}
	if err := CheckName(root, "web", "2"); err != nil {
		t.Fatal(err)
	}
	if a.Running() {
		t.Fatal("not started, but running")
	}
}
