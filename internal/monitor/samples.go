package monitor

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"time"
)

// Sample is one ping: when it was sent and how long the reply took, or
// RTT < 0 when no reply came.
type Sample struct {
	At  time.Time
	RTT time.Duration
}

// Lost says whether the ping went unanswered.
func (s Sample) Lost() bool { return s.RTT < 0 }

// Ms is the round trip in milliseconds.
func (s Sample) Ms() float64 { return float64(s.RTT) / float64(time.Millisecond) }

func formatSample(s Sample) []byte {
	rtt := int64(-1)
	if s.RTT >= 0 {
		rtt = s.RTT.Microseconds()
	}
	b := strconv.AppendInt(nil, s.At.UnixMilli(), 10)
	b = append(b, ' ')
	b = strconv.AppendInt(b, rtt, 10)
	return append(b, '\n')
}

// parseSamples reads whole lines from b and returns the samples and how
// many bytes they took, leaving a partly written last line for next time.
func parseSamples(b []byte, into []Sample) ([]Sample, int) {
	used := 0
	for {
		nl := bytes.IndexByte(b[used:], '\n')
		if nl < 0 {
			return into, used
		}
		line := b[used : used+nl]
		used += nl + 1
		sp := bytes.IndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		ms, err1 := strconv.ParseInt(string(line[:sp]), 10, 64)
		us, err2 := strconv.ParseInt(string(line[sp+1:]), 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		s := Sample{At: time.UnixMilli(ms), RTT: -1}
		if us >= 0 {
			s.RTT = time.Duration(us) * time.Microsecond
		}
		into = append(into, s)
	}
}

// History follows a monitor's samples file, reading only what's new each
// time it's refreshed.
type History struct {
	Path    string
	Samples []Sample
	// tail, when above 0, is how many bytes from the end the first read
	// starts at, so a preview needn't load weeks of history.
	tail   int64
	offset int64
	size   int64
}

// NewHistory follows path. tail > 0 reads only about that many bytes of
// the past.
func NewHistory(path string, tail int64) *History {
	return &History{Path: path, tail: tail}
}

// Size is how big the file was when last refreshed.
func (h *History) Size() int64 { return h.size }

// Full says whether the whole file has been read.
func (h *History) Full() bool { return h.tail <= 0 }

// LoadAll switches to the whole history, reading it on the next refresh.
func (h *History) LoadAll() {
	if h.tail > 0 {
		h.tail, h.offset, h.Samples = 0, 0, nil
	}
}

// Refresh reads whatever was added since last time. A file that shrank
// was trimmed by the monitor, so it's read again from the start.
func (h *History) Refresh() {
	f, err := os.Open(h.Path)
	if err != nil {
		h.Samples, h.offset, h.size = nil, 0, 0
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	size := fi.Size()
	if size < h.offset || (size < h.size) {
		h.Samples, h.offset = nil, 0
	}
	h.size = size
	start := h.offset
	skipPartial := false
	if start == 0 && h.tail > 0 && size > h.tail {
		start, skipPartial = size-h.tail, true
	}
	if size <= start {
		return
	}
	buf := make([]byte, size-start)
	n, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return
	}
	buf = buf[:n]
	if skipPartial {
		nl := bytes.IndexByte(buf, '\n')
		if nl < 0 {
			return
		}
		buf, start = buf[nl+1:], start+int64(nl+1)
	}
	var used int
	h.Samples, used = parseSamples(buf, h.Samples)
	h.offset = start + int64(used)
}

// Since returns the samples at or after t; they're in time order.
func Since(s []Sample, t time.Time) []Sample {
	return s[SearchTime(s, t):]
}

// SearchTime is the index of the first sample at or after t.
func SearchTime(s []Sample, t time.Time) int {
	lo, hi := 0, len(s)
	for lo < hi {
		mid := (lo + hi) / 2
		if s[mid].At.Before(t) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// Stats summarises some samples.
type Stats struct {
	Sent, Lost    int
	Min, Avg, Max float64 // ms, over replies
	Jitter        float64 // mean difference between consecutive replies, ms
}

// LossPct is the share of pings lost, 0 to 100.
func (s Stats) LossPct() float64 {
	if s.Sent == 0 {
		return 0
	}
	return 100 * float64(s.Lost) / float64(s.Sent)
}

// Summarise works out stats for samples.
func Summarise(samples []Sample) Stats {
	var st Stats
	var sum, jsum float64
	var prev float64
	got, jn := 0, 0
	for _, s := range samples {
		st.Sent++
		if s.Lost() {
			st.Lost++
			continue
		}
		ms := s.Ms()
		if got == 0 || ms < st.Min {
			st.Min = ms
		}
		if ms > st.Max {
			st.Max = ms
		}
		if got > 0 {
			d := ms - prev
			if d < 0 {
				d = -d
			}
			jsum += d
			jn++
		}
		prev = ms
		sum += ms
		got++
	}
	if got > 0 {
		st.Avg = sum / float64(got)
	}
	if jn > 0 {
		st.Jitter = jsum / float64(jn)
	}
	return st
}
