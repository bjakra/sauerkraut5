//go:build linux && !dot

// screenbench times echod's own screen path on the device's panel, the cores' or the GPU's, with the
// same frames: how long a frame takes to reach the panel, and how much of the cores a steady frame rate
// costs. It is the measurement behind choosing one path over the other on a board.
//
// It takes the panel: stop echod first (touch /run/techo5/hold; killall techo5), and remove the hold
// after.
//
//	screenbench [-helper /usr/local/bin/techo5-gpu | -helper ""] [-fps 20] [-seconds 10] [-kind all|full|tenth|none] [-steady]
//
// With a helper the screen is drawn by the GPU as echod would have it; with -helper "" by the cores.
// Three kinds of frame are shown: every pixel new (a video, a page sliding), a clock's worth of change
// (a band a tenth of the screen), and nothing new at all. -steady leaves out the flat-out timing and
// only holds the frame rate, for watching what else on the device makes of that load: the wake word's
// busy_pct in echod's log, say, with echod left running on the GPU path, which draws every frame whole
// and so puts its own picture back once this is done (on the cores it would not: do not try it there).
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/screen"
)

func main() {
	helper := flag.String("helper", "/usr/local/bin/techo5-gpu", "the GPU helper; empty to draw on the cores")
	fps := flag.Float64("fps", 20, "frames a second for the load measurement")
	seconds := flag.Float64("seconds", 10, "how long each measurement runs")
	kind := flag.String("kind", "all", "the frames to show: all, full, tenth or none")
	steady := flag.Bool("steady", false, "only hold the frame rate; no flat-out timing")
	flag.Parse()
	if err := run(*helper, *fps, *seconds, *kind, *steady); err != nil {
		fmt.Fprintln(os.Stderr, "screenbench:", err)
		os.Exit(1)
	}
}

// busy is the jiffies all cores have spent not idle, and all of them, from /proc/stat.
func busy() (work, total float64) {
	b, _ := os.ReadFile("/proc/stat")
	line, _, _ := strings.Cut(string(b), "\n")
	for i, f := range strings.Fields(line)[1:] {
		v, _ := strconv.ParseFloat(f, 64)
		total += v
		if i != 3 && i != 4 { // idle, iowait
			work += v
		}
	}
	return work, total
}

// own is the CPU time this process has used, in seconds.
func own() float64 {
	var r unix.Rusage
	_ = unix.Getrusage(unix.RUSAGE_SELF, &r)
	return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
}

func run(helper string, fps, seconds float64, only string, steady bool) error {
	if helper == "" {
		helper = "/nonexistent/techo5-gpu"
	}
	gpu.SetHelperPath(helper)
	d, err := screen.Open()
	if err != nil {
		return err
	}
	defer d.Close()
	path := "the cores"
	if screen.DrawnByGPU() {
		path = "the GPU (" + gpu.Name() + ")"
	}
	canvas := d.Canvas()
	w, h := d.Size()
	fmt.Printf("screen   %v, canvas %dx%d, drawn by %s\n", d, w, h, path)

	// Eight pictures made beforehand, so that making one is not what is timed: copying one onto the
	// canvas is all a frame costs here besides showing it.
	const pictures = 8
	pics := make([][]byte, pictures)
	for f := range pics {
		pics[f] = make([]byte, len(canvas.Pix))
		for y := range h {
			row := pics[f][y*canvas.Stride:]
			for x := range w {
				row[x*4] = uint8(x + f*37)
				row[x*4+1] = uint8(y + f*59)
				row[x*4+2] = uint8((x>>4)*16 + y>>4 + f*83)
				row[x*4+3] = 255
			}
		}
	}
	band := h / 10
	kinds := []struct {
		flag, name string
		draw       func(f int)
	}{
		{"full", "every pixel new", func(f int) { copy(canvas.Pix, pics[f%pictures]) }},
		{"tenth", "a tenth new", func(f int) {
			at := (h/2 - band/2) * canvas.Stride
			copy(canvas.Pix[at:at+band*canvas.Stride], pics[f%pictures][at:])
		}},
		{"none", "nothing new", func(int) {}},
	}

	frames := int(fps * seconds)
	period := time.Duration(float64(time.Second) / fps)

	// What the device is busy with when nothing is drawn, to read the figures below against. Jiffies
	// are hundredths of a second, so those not idle over the seconds gone are a percentage of one core
	// however many cores are awake.
	w0, _ := busy()
	began := time.Now()
	time.Sleep(time.Duration(seconds * float64(time.Second)))
	w1, _ := busy()
	fmt.Printf("%-16s %.1f%% of one core busy on the whole device\n", "nothing drawn", (w1-w0)/time.Since(began).Seconds())
	for _, k := range kinds {
		if only != "all" && only != k.flag {
			continue
		}
		// What the frames cost to make, without showing them.
		c0, start := own(), time.Now()
		for f := range frames {
			k.draw(f)
		}
		makeCPU, makeTime := own()-c0, time.Since(start)

		// As fast as they go: the time from Present to the frame being on the panel.
		times := make([]float64, 0, frames)
		start = time.Now()
		for f := 0; f < frames && !steady; f++ {
			k.draw(f)
			a := time.Now()
			if err := d.Present(); err != nil {
				return err
			}
			if err := d.Settle(); err != nil {
				return err
			}
			times = append(times, float64(time.Since(a).Microseconds())/1000)
		}
		if !steady {
			flat := time.Since(start) - makeTime
			sort.Float64s(times)
			var sum float64
			for _, t := range times {
				sum += t
			}
			fmt.Printf("%-16s to the panel: mean %.2f ms, median %.2f, 95%% %.2f, worst %.2f (%.0f fps flat out)\n", k.name,
				sum/float64(frames), times[frames/2], times[frames*95/100], times[frames-1], float64(frames)/flat.Seconds())
		}

		// At a steady rate: what the cores are left with.
		time.Sleep(300 * time.Millisecond)
		w0, _ := busy()
		c0 = own()
		next := time.Now()
		began := next
		for f := range frames {
			k.draw(f)
			if err := d.Present(); err != nil {
				return err
			}
			if err := d.Settle(); err != nil {
				return err
			}
			next = next.Add(period)
			time.Sleep(time.Until(next))
		}
		w1, _ := busy()
		paced := time.Since(began).Seconds()
		self := own() - c0 - makeCPU
		fmt.Printf("%-16s at %.0f fps: %.1f%% of one core busy on the whole device, %.2f ms of this process's CPU a frame\n", k.name,
			fps, (w1-w0)/paced, 1000*self/float64(frames))
	}
	if screen.DrawnByGPU() != (path != "the cores") {
		return fmt.Errorf("the screen changed paths during the run")
	}
	return nil
}
