//go:build !dot && !panel

package screen

import (
	"log/slog"
	"time"
)

// framesEvery is how often the frames shown are summed up in the log, when any were.
const framesEvery = 30 * time.Second

// frameStats sums up the frames Present put on the panel: how many, how long each kept the screen
// busy (on the cores the turn onto the page, in the background; on the GPU the call itself, which
// returns once the helper has the canvas unless the lift needs the frame on the panel first), and
// which path drew them. Like the wake word's line it is what tells, on a
// device in use, whether the screen's path is the one it should be and what it costs.
type frameStats struct {
	since      time.Time
	count      int
	sum, worst time.Duration
	gpu, cores int
}

// add counts a frame that took took to reach the panel, drawn by the GPU or by the cores, and writes
// the line once framesEvery has passed.
func (s *frameStats) add(took time.Duration, byGPU bool) {
	now := time.Now()
	if s.count == 0 {
		s.since = now.Add(-took)
	}
	s.count++
	s.sum += took
	s.worst = max(s.worst, took)
	if byGPU {
		s.gpu++
	} else {
		s.cores++
	}
	if now.Sub(s.since) < framesEvery {
		return
	}
	by := "cores"
	switch {
	case s.cores == 0:
		by = "gpu"
	case s.gpu != 0:
		by = "both"
	}
	slog.Info("screen frames", "count", s.count, "mean_ms", float64(s.sum.Microseconds())/float64(s.count)/1000,
		"worst_ms", float64(s.worst.Microseconds())/1000, "drawn_by", by)
	*s = frameStats{}
}
