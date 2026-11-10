//go:build !dot && !spot && !panel

package display

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/screen"
)

// TestTurnBenchOnDevice times the turn screens as the daemon draws them, with its own renderer, on a
// unit's own panel: what a frame costs to draw and to show, the frame rate the daemon's pacing then
// gives, and the processor time it takes. It is how the cores' screen path and the GPU's are compared
// on real content rather than on test patterns (cmd/screenbench).
//
// It runs only where asked, on a unit, with the daemon held (touch /run/techo5/hold; killall techo5):
//
//	GOOS=linux GOARCH=arm GOARM=7 go test -c -o turnbench ./internal/feature/display
//	TECHO5_TURN_BENCH=6 [TECHO5_TURN_BENCH_HELPER=<techo5-gpu, or "none" for the cores>] ./turnbench -test.run TurnBenchOnDevice -test.v
//
// TECHO5_TURN_BENCH is the seconds each measurement runs. The microphone is not read here, so the
// bars and Rainbow's light are drawn for a quiet room.
func TestTurnBenchOnDevice(t *testing.T) {
	seconds, _ := strconv.ParseFloat(os.Getenv("TECHO5_TURN_BENCH"), 64)
	if seconds <= 0 {
		t.Skip("TECHO5_TURN_BENCH is not set: a measurement for a unit, not a test")
	}
	switch h := os.Getenv("TECHO5_TURN_BENCH_HELPER"); h {
	case "":
	case "none":
		gpu.SetHelperPath("/nonexistent/techo5-gpu")
	default:
		gpu.SetHelperPath(h)
	}
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	dev, err := screen.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	by := "the cores"
	if screen.DrawnByGPU() {
		by = "the GPU (" + gpu.Name() + ")"
	}
	fmt.Printf("screen: %v, drawn by %s\n", dev, by)

	// An afternoon, so that no night face or night cue is drawn in Rainbow's place.
	day := time.Date(2026, time.October, 31, 15, 0, 0, 0, time.Local)
	listen, listenPk := eqVoice(0.62, 1)
	reply, replyPk := eqVoice(0.95, 7)
	const heard, answer = "What's the weather tomorrow?", "Tomorrow will be sunny, with a high of 74 and a low of 51."
	cases := []struct {
		name, style string
		frame       time.Duration // the daemon's pace for it
		scene       func(at time.Time) scene
	}{
		{"clock (idle page)", "", idleFrame, func(at time.Time) scene { return scene{now: at, phase: "idle"} }},
		{"bars, listening", "", eqFrame, func(at time.Time) scene {
			return scene{now: at, phase: "listening", since: day, eq: &eqView{level: listen, peak: listenPk}}
		}},
		{"bars, replying", "", eqFrame, func(at time.Time) scene {
			return scene{now: at, phase: "replying", since: day, heard: heard, reply: answer, eq: &eqView{level: reply, peak: replyPk}}
		}},
		{"rainbow, listening", "rainbow", rainbowFrame, func(at time.Time) scene {
			return scene{now: at, phase: "idle", cue: "listening", since: day}
		}},
		{"rainbow, replying", "rainbow", rainbowFrame, func(at time.Time) scene {
			return scene{now: at, phase: "idle", cue: "replying", since: day, heard: heard, reply: answer}
		}},
	}
	for _, c := range cases {
		if err := config.Set().Screen().TurnStyle(c.style); err != nil {
			t.Fatal(err)
		}
		r := newRenderer(dev.Canvas())
		// Shown as the daemon's frame shows it: Rainbow's turn through the GPU where it can be.
		d := &Display{dev: dev, r: r}
		for _, paced := range []bool{false, true} {
			if c.frame == idleFrame && paced {
				continue // once a second: nothing to learn from pacing it
			}
			var draw, show time.Duration
			frames, effects := 0, 0
			busy0, cpu0, start := deviceBusy(), ownCPU(), time.Now()
			for time.Since(start) < time.Duration(seconds*float64(time.Second)) {
				began := time.Now()
				r.rainbowGPUOK = d.rainbowGPUOK()
				r.draw(c.scene(day.Add(time.Since(start))))
				drawn := time.Now()
				if d.presentRainbow() {
					effects++
				} else if err := d.present(); err != nil {
					t.Fatal(err)
				}
				draw += drawn.Sub(began)
				show += time.Since(drawn)
				frames++
				if paced {
					// The daemon's pace: from one frame's start to the next's, and Rainbow's breath
					// after a frame that took all of it.
					time.Sleep(max(c.frame-time.Since(began), rainbowBreath))
				}
			}
			if err := dev.Settle(); err != nil {
				t.Fatal(err)
			}
			took := time.Since(start).Seconds()
			how := "flat out"
			if paced {
				how = fmt.Sprintf("paced %d ms", c.frame.Milliseconds())
			}
			fmt.Printf("%-20s %-12s %5.1f fps; draw %5.1f ms, Present %5.1f ms a frame; this process %5.1f ms of CPU a frame; device %3.0f%% of one core busy",
				c.name, how, float64(frames)/took, ms(draw)/float64(frames), ms(show)/float64(frames),
				1000*(ownCPU()-cpu0)/float64(frames), (deviceBusy()-busy0)/took)
			if effects > 0 {
				fmt.Printf("; %d of %d frames' turn drawn by the GPU", effects, frames)
			}
			fmt.Println()
		}
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// deviceBusy is the hundredths of a second all cores together have spent not idle (/proc/stat).
func deviceBusy() float64 {
	b, _ := os.ReadFile("/proc/stat")
	line, _, _ := strings.Cut(string(b), "\n")
	var work float64
	for i, f := range strings.Fields(line)[1:] {
		if i != 3 && i != 4 { // idle, iowait
			v, _ := strconv.ParseFloat(f, 64)
			work += v
		}
	}
	return work
}

// ownCPU is the processor time this process has used, in seconds.
func ownCPU() float64 {
	var r unix.Rusage
	_ = unix.Getrusage(unix.RUSAGE_SELF, &r)
	return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
}
