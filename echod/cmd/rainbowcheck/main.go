//go:build linux && !dot && !sk5_no_rainbow

// rainbowcheck draws a Rainbow turn (lib/rainbow) twice, frame for frame from the same moments: on the
// cores, as Finish draws it, and through the GPU helper, as FinishGPU and the shader draw it, and
// compares what reaches the panel with the cores' picture. It also times the GPU's frames.
//
// The panel is a file standing in for the framebuffer (the helper's TECHO5_GPU_FB_TEST), so echod can go
// on running, and the GPU's work is timed without the wait for the panel. -out writes some frames of
// both as PNGs.
//
//	rainbowcheck -helper /usr/local/bin/techo5-gpu [-size 480x960] [-out dir] [-frame|-light|-card file]
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/lib/fonts"
	"github.com/HuskerMinion/techo5/echod/internal/lib/rainbow"
	"github.com/HuskerMinion/techo5/echod/internal/lib/rainbow/ongpu"
)

func main() {
	helper := flag.String("helper", "/usr/local/bin/techo5-gpu", "the helper to check")
	size := flag.String("size", "480x960", "the stand-in panel's size, WxH (portrait: the canvas is it turned)")
	out := flag.String("out", "", "a directory to write some frames of both to, as PNGs")
	var shaders [rainbow.GPUShaders]*string
	for i, name := range []string{"frame", "light", "card"} {
		shaders[i] = flag.String(name, "", "a file with the "+name+" shader to use in place of the Rainbow's own, to try a change")
	}
	flag.Parse()
	for i, file := range shaders {
		if *file == "" {
			continue
		}
		b, err := os.ReadFile(*file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "rainbowcheck:", err)
			os.Exit(1)
		}
		rainbow.Shaders[i] = string(b)
	}
	if err := run(*helper, *size, *out); err != nil {
		fmt.Fprintln(os.Stderr, "rainbowcheck:", err)
		os.Exit(1)
	}
}

// page is a page to draw the turn over: smooth color, with sharp bars across it, so that both the
// glass's blur and the light's edges show.
func page(img *image.RGBA) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	for y := range h {
		row := img.Pix[y*img.Stride:]
		for x := range w {
			r, g, b := 30+x*120/w, 40+y*100/h, 90+(x+y)*60/(w+h)
			if (y/24)%5 == 0 && x%200 < 150 {
				r, g, b = 230, 220, 200
			}
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = uint8(r), uint8(g), uint8(b), 255
		}
	}
}

// step is one frame of the check: its moment from the turn's start and what the turn is then.
type step struct {
	at   time.Duration
	turn rainbow.Turn
	shot string // a name to write it under with -out
}

func script() []step {
	var s []step
	frame := 66 * time.Millisecond
	heard := "what is the weather like tomorrow in berlin"
	reply := "Tomorrow in Berlin it will be cloudy with a chance of rain in the afternoon, highs around 12 degrees."
	for t := time.Duration(0); t < 9*time.Second; t += frame {
		sec := t.Seconds()
		var tr rainbow.Turn
		switch {
		case sec < 3.5:
			tr = rainbow.Turn{Phase: "listening", Level: 0.5 + 0.4*math.Sin(sec*7), Heard: heard[:min(len(heard), int(sec*20))]}
		case sec < 5:
			tr = rainbow.Turn{Phase: "thinking", Heard: heard}
		default:
			tr = rainbow.Turn{Phase: "replying", Level: 0.4 + 0.3*math.Sin(sec*5), Heard: heard, Reply: reply}
		}
		st := step{at: t, turn: tr}
		switch {
		case t == 30*frame:
			st.shot = "listening"
		case t == 66*frame:
			st.shot = "thinking"
		case t == 120*frame:
			st.shot = "replying"
		}
		s = append(s, st)
	}
	return s
}

func run(helper, size, out string) error {
	gpu.SetHelperPath(helper)
	ws, hs, ok := strings.Cut(size, "x")
	pw, _ := strconv.Atoi(ws)
	ph, _ := strconv.Atoi(hs)
	if !ok || pw <= 0 || ph <= 0 {
		return fmt.Errorf("a size of %q", size)
	}
	f, err := os.CreateTemp("", "rainbowcheck-fb-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Truncate(int64(pw * ph * 4)); err != nil {
		return err
	}
	fb, err := unix.Mmap(int(f.Fd()), 0, pw*ph*4, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return err
	}
	os.Setenv("TECHO5_GPU_FB_TEST", fmt.Sprintf("%dx%d:%s", pw, ph, f.Name()))
	if !gpu.Available() {
		return errors.New("the helper did not start")
	}
	// The canvas is the panel turned a quarter: canvas (x, y) is on the panel at (pw-1-y, x).
	cw, ch := ph, pw
	disp, canvas, err := gpu.OpenDisplay(cw, ch, true)
	if err != nil {
		return err
	}
	defer disp.Close()
	fmt.Printf("gpu      %s\npanel    %dx%d, canvas %dx%d\n", gpu.Name(), pw, ph, cw, ch)

	name := fonts.Names()[0]
	tiny, err := fonts.Face(name, false, 22)
	if err != nil {
		return err
	}
	small, err := fonts.Face(name, false, 26)
	if err != nil {
		return err
	}
	faces := rainbow.Faces{Heard: tiny, Reply: small}

	var drawer ongpu.Drawer
	defer drawer.Close()

	pg := image.NewRGBA(image.Rect(0, 0, cw, ch))
	page(pg)
	want := image.NewRGBA(pg.Rect)
	shown := image.NewRGBA(pg.Rect)
	var cpu, onGPU rainbow.Screen
	var fr rainbow.GPUFrame
	start := time.Unix(1_700_000_000, 0)
	var cores, gpus int
	var times []float64
	var bad, worst int
	var sumDiff float64
	var pixels int
	for _, st := range script() {
		now := start.Add(st.at)
		cpu.Begin(cw, ch, now, st.turn)
		copy(want.Pix, pg.Pix)
		cpu.Finish(want, faces)

		onGPU.Begin(cw, ch, now, st.turn)
		copy(canvas.Pix, pg.Pix)
		if _, ok := onGPU.FinishGPU(canvas, faces, &fr); !ok {
			onGPU.Finish(canvas, faces)
			cores++
			if err := disp.Present(nil); err != nil {
				return err
			}
			continue
		}
		prog, params, reads, passes, err := drawer.Frame(&fr)
		if err != nil {
			return err
		}
		a := time.Now()
		if err := disp.PresentEffect(nil, prog, params, reads, false, false, passes...); err != nil {
			var refused *gpu.ProgramError
			if errors.As(err, &refused) {
				fmt.Println(refused.Log)
			}
			return err
		}
		times = append(times, float64(time.Since(a).Microseconds())/1000)
		drawer.Shown()
		gpus++
		for y := range ch {
			for x := range cw {
				i := (x*pw + pw - 1 - y) * 4
				o := y*shown.Stride + x*4
				shown.Pix[o], shown.Pix[o+1], shown.Pix[o+2], shown.Pix[o+3] = fb[i+2], fb[i+1], fb[i], 255
			}
		}
		b, w, s := diff(shown, want, 24)
		if w > worst && out != "" {
			st.shot = "worst"
			fmt.Printf("worst    so far %d, %v into the turn (%s)\n", w, st.at, st.turn.Phase)
		}
		bad, worst, sumDiff, pixels = bad+b, max(worst, w), sumDiff+s, pixels+cw*ch
		if st.shot != "" && out != "" {
			if err := save(filepath.Join(out, st.shot+"-cores.png"), want); err != nil {
				return err
			}
			if err := save(filepath.Join(out, st.shot+"-gpu.png"), shown); err != nil {
				return err
			}
		}
	}
	fmt.Printf("frames   %d on the GPU, %d on the cores (the entry, and any before the GPU's geometry was ready)\n", gpus, cores)
	if gpus == 0 {
		return errors.New("no frame was drawn by the GPU")
	}
	sort.Float64s(times)
	fmt.Printf("present  mean %.2f ms, median %.2f, 95%% %.2f, worst %.2f (with the textures sent)\n",
		mean(times), times[len(times)/2], times[len(times)*95/100], times[len(times)-1])
	fmt.Printf("compare  mean difference %.2f of 255 a channel, %.3f%% of pixels off by more than 24, largest %d\n",
		sumDiff/float64(pixels), 100*float64(bad)/float64(pixels), worst)
	return nil
}

// diff compares got with want: the pixels off by more than slack in a channel, the largest difference,
// and the sum over the pixels of the mean difference of their channels.
func diff(got, want *image.RGBA, slack int) (bad, worst int, sum float64) {
	for i := 0; i < len(got.Pix); i += 4 {
		d0, d1, d2 := absInt(int(got.Pix[i])-int(want.Pix[i])), absInt(int(got.Pix[i+1])-int(want.Pix[i+1])), absInt(int(got.Pix[i+2])-int(want.Pix[i+2]))
		d := max(d0, d1, d2)
		if d > slack {
			bad++
		}
		worst = max(worst, d)
		sum += float64(d0+d1+d2) / 3
	}
	return bad, worst, sum
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func mean(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func save(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
