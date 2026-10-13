package cli

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	"pingy/internal/monitor"
)

// highMs is the round trip above which a bar turns from green to yellow.
const highMs = 100

// blocks are the eighths a bar's top cell is drawn with.
var blocks = []string{" ", "▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

// zoomSteps are the times one column of a graph can stand for.
var zoomSteps = []time.Duration{
	time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second,
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute,
	time.Hour, 3 * time.Hour,
}

// bucket gathers the pings that fall in one column.
type bucket struct {
	n, lost int
	sum     float64 // ms, over replies
}

func (b bucket) avg() float64 {
	if got := b.n - b.lost; got > 0 {
		return b.sum / float64(got)
	}
	return 0
}

// bucketise sorts samples into cols columns of step each, ending at end.
func bucketise(samples []monitor.Sample, end time.Time, step time.Duration, cols int) []bucket {
	bs := make([]bucket, max(cols, 0))
	start := end.Add(-time.Duration(cols) * step)
	for _, s := range samples[monitor.SearchTime(samples, start):] {
		if !s.At.Before(end) {
			break
		}
		i := int(s.At.Sub(start) / step)
		if i < 0 || i >= cols {
			continue
		}
		bs[i].n++
		if s.Lost() {
			bs[i].lost++
		} else {
			bs[i].sum += s.Ms()
		}
	}
	return bs
}

// niceCeil rounds v up to 1, 2, 2.5 or 5 times a power of ten.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*p >= v-1e-9 {
			return m * p
		}
	}
	return 10 * p
}

// fmtMs formats a round trip for labels and stats.
func fmtMs(v float64) string {
	switch {
	case v >= 10000:
		return fmt.Sprintf("%.0fs", v/1000)
	case v >= 100:
		return fmt.Sprintf("%.0fms", v)
	case v >= 10:
		return fmt.Sprintf("%.1fms", v)
	default:
		return fmt.Sprintf("%.2fms", v)
	}
}

// fmtAxis is fmtMs without needless decimals, for the y axis.
func fmtAxis(v float64) string {
	switch {
	case v >= 10000:
		return fmt.Sprintf("%gs", v/1000)
	default:
		s := fmt.Sprintf("%gms", math.Round(v*100)/100)
		if len(s) > gutter-1 {
			s = fmt.Sprintf("%.1fms", v)
		}
		return s
	}
}

// cell is one character of a graph and its colour (nil for none).
type cell struct {
	s string
	c color.Color
}

// joinCells renders a row, colouring runs of the same colour together.
func (u ui) joinCells(cells []cell) string {
	var b, run strings.Builder
	var cur color.Color
	flush := func() {
		if cur == nil {
			b.WriteString(run.String())
		} else {
			b.WriteString(u.fg(cur, run.String()))
		}
		run.Reset()
	}
	for i, c := range cells {
		if i > 0 && c.c != cur {
			flush()
		}
		cur = c.c
		run.WriteString(c.s)
	}
	flush()
	return b.String()
}

const gutter = 7 // y-axis label width, before the axis line

// graph draws samples as a bar chart w wide and h tall, including its
// axes: one column per step, the last ending at end. Bars are the average
// round trip, red where pings were lost, and a red band where all were.
func (u ui) graph(samples []monitor.Sample, end time.Time, step time.Duration, w, h int) []string {
	plotW, barH := w-gutter-1, h-2
	if plotW < 4 || barH < 1 {
		return nil
	}
	end = localTrunc(end, step)
	bs := bucketise(samples, end, step, plotW)
	top := 0.0
	for _, b := range bs {
		top = max(top, b.avg())
	}
	scale := niceCeil(top * 1.1)

	lines := make([]string, 0, h)
	for r := 0; r < barH; r++ {
		fromBottom := barH - 1 - r
		label := ""
		switch {
		case r == 0:
			label = fmtAxis(scale)
		case barH >= 5 && r == barH/2:
			label = fmtAxis(scale / 2)
		}
		cells := make([]cell, plotW)
		for i, b := range bs {
			cells[i] = cell{s: " "}
			switch {
			case b.n == 0:
			case b.lost == b.n:
				cells[i] = cell{"░", colBad}
			default:
				eighths := int(math.Round(b.avg() / scale * float64(barH*8)))
				eighths = min(max(eighths, 1), barH*8)
				c := colOK
				if b.lost > 0 {
					c = colBad
				} else if b.avg() > highMs {
					c = colWarn
				}
				if part := eighths - fromBottom*8; part > 0 {
					cells[i] = cell{blocks[min(part, 8)], c}
				}
			}
		}
		lines = append(lines, u.faint(fmt.Sprintf("%*s ", gutter-1, label)+axisChar(label))+u.joinCells(cells))
	}

	// The time axis: ticks where a round time falls, labelled when there's room.
	tick := zoomSteps[len(zoomSteps)-1]
	for _, t := range []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second,
		time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
		time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 48 * time.Hour} {
		if t >= step*14 {
			tick = t
			break
		}
	}
	start := end.Add(-time.Duration(plotW) * step)
	axis := []rune(strings.Repeat("─", plotW))
	labels := []rune(strings.Repeat(" ", plotW))
	free := 0
	for i := 1; i < plotW; i++ {
		a, b := localTrunc(start.Add(time.Duration(i-1)*step), tick), localTrunc(start.Add(time.Duration(i)*step), tick)
		if a.Equal(b) {
			continue
		}
		axis[i] = '┬'
		l := []rune(tickLabel(b, tick))
		if i >= free && i+len(l) <= plotW {
			copy(labels[i:], l)
			free = i + len(l) + 2
		}
	}
	lines = append(lines,
		u.faint(fmt.Sprintf("%*s └", gutter-1, "0")+string(axis)),
		strings.Repeat(" ", gutter+1)+u.faint(string(labels)))
	return lines
}

func axisChar(label string) string {
	if label != "" {
		return "┤"
	}
	return "│"
}

// localTrunc rounds t down to a multiple of d in local time. Graphs end
// on a whole step, so columns hold the same pings from one redraw to the
// next, and the column still filling isn't shown.
func localTrunc(t time.Time, d time.Duration) time.Time {
	_, off := t.Zone()
	shift := time.Duration(off) * time.Second
	return t.Add(shift).Truncate(d).Add(-shift)
}

func tickLabel(t time.Time, tick time.Duration) string {
	t = t.Local()
	switch {
	case tick < time.Minute:
		return t.Format("15:04:05")
	case tick < 24*time.Hour && !(t.Hour() == 0 && t.Minute() == 0):
		return t.Format("15:04")
	default:
		return t.Format("Mon 2")
	}
}

// sparkline is a one-row graph of samples, scaled to its own tallest bar.
func (u ui) sparkline(samples []monitor.Sample, end time.Time, step time.Duration, w int) string {
	end = localTrunc(end, step)
	bs := bucketise(samples, end, step, w)
	top := 0.0
	for _, b := range bs {
		top = max(top, b.avg())
	}
	cells := make([]cell, w)
	for i, b := range bs {
		switch {
		case b.n == 0:
			cells[i] = cell{s: " "}
		case b.lost == b.n:
			cells[i] = cell{"×", colBad}
		default:
			c := colOK
			if b.lost > 0 {
				c = colBad
			} else if b.avg() > highMs {
				c = colWarn
			}
			lvl := 1
			if top > 0 {
				lvl = min(max(int(math.Round(b.avg()/top*8)), 1), 8)
			}
			cells[i] = cell{blocks[lvl], c}
		}
	}
	return u.joinCells(cells)
}

// fmtStep says what one column stands for, e.g. "5s" or "1m".
func fmtStep(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return d.String()
}

// fmtAgo is a rough "how long", e.g. 3m or 2h5m.
func fmtAgo(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		if m := int(d.Minutes()) % 60; m > 0 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	days := int(d.Hours()) / 24
	if h := int(d.Hours()) % 24; h > 0 {
		return fmt.Sprintf("%dd%dh", days, h)
	}
	return fmt.Sprintf("%dd", days)
}
