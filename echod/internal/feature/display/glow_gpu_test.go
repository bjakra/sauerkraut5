//go:build linux && !dot && !spot && !panel

package display

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
)

// TestGlowOnGPU checks the Glow moving by itself on a real GPU helper against the cores' colors at the
// same moment: a page of the Glow's colors alone at t0, shown as the effect, read back from a file
// standing in for the panel a second later, against glowField at t0 plus that second. It also says how
// far the colors moved, which the GPU's picture has to follow.
//
//	TECHO5_GLOW_CHECK=<techo5-gpu> go test -run GlowOnGPU ./internal/feature/display
func TestGlowOnGPU(t *testing.T) {
	helper := os.Getenv("TECHO5_GLOW_CHECK")
	if helper == "" {
		t.Skip("TECHO5_GLOW_CHECK is not set: a check against a real helper")
	}
	gpu.SetHelperPath(helper)
	const pw, ph = 480, 960 // the Show 5's panel; the canvas is it turned
	cw, ch := ph, pw
	f, err := os.CreateTemp("", "glowcheck-fb-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if err := f.Truncate(pw * ph * 4); err != nil {
		t.Fatal(err)
	}
	fb, err := unix.Mmap(int(f.Fd()), 0, pw*ph*4, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TECHO5_GPU_FB_TEST", fmt.Sprintf("%dx%d:%s", pw, ph, f.Name()))
	disp, canvas, err := gpu.OpenDisplay(cw, ch, true)
	if err != nil {
		t.Fatal(err)
	}
	defer disp.Close()

	ground, colors := color.RGBA{40, 30, 24, 255}, [3]color.RGBA{{255, 176, 64, 255}, {230, 90, 40, 255}, {150, 120, 90, 255}}
	fw, fh := cw/glowCell+1, ch/glowCell+1
	var cores glowBuffers
	paint := func(img *image.RGBA, at time.Time) {
		cores.stretch(img, cores.glowField(fw, fh, at, ground, colors), nil)
	}
	shown := func() *image.RGBA {
		img := image.NewRGBA(canvas.Rect)
		for y := range ch {
			for x := range cw {
				i := (x*pw + pw - 1 - y) * 4
				o := y*img.Stride + x*4
				img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = fb[i+2], fb[i+1], fb[i], 255
			}
		}
		return img
	}
	diff := func(a, b *image.RGBA) (mean float64, worst int) {
		for i := 0; i < len(a.Pix); i += 4 {
			for c := range 3 {
				d := int(a.Pix[i+c]) - int(b.Pix[i+c])
				if d < 0 {
					d = -d
				}
				mean += float64(d)
				worst = max(worst, d)
			}
		}
		return mean / float64(len(a.Pix)/4*3), worst
	}

	// A moment where the blobs move fast across the screen.
	t0 := time.Unix(1_700_000_037, 0)
	paint(canvas, t0)
	var g glowGPU
	prog, params, reads, passes, err := g.effect(glowShown{drawn: true, at: t0, fw: fw, fh: fh, ground: ground, colors: colors})
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if err := disp.PresentMoving(canvas, prog, params, reads, false, false, glowEvery, glowLasts, passes...); err != nil {
		t.Fatal(err)
	}
	first := shown()
	if m, w := diff(first, canvas); w > 1 {
		t.Errorf("at its start the frame is not the page: mean %.2f, largest %d", m, w)
	}
	// What the helper's moving costs the processor, from its own count of the time it has run.
	c0 := childCPU()
	time.Sleep(1500 * time.Millisecond)
	got, readAt := shown(), time.Since(began)
	cpu := (childCPU() - c0) / readAt.Seconds() * 100
	// The frame on the panel was drawn up to a glowEvery ago.
	var best float64 = 1e9
	var bestAt time.Duration
	var bestWorst int
	want := image.NewRGBA(canvas.Rect)
	for back := time.Duration(0); back <= glowEvery+20*time.Millisecond; back += 5 * time.Millisecond {
		at := readAt - back
		paint(want, t0.Add(at))
		if m, w := diff(got, want); m < best {
			best, bestAt, bestWorst = m, at, w
		}
	}
	moved, movedWorst := diff(got, first)
	fmt.Printf("glow: the colors moved by a mean %.2f (largest %d) of 255 in %v; against the cores' at %v: mean %.2f, largest %d; the helper %.1f%% of one core\n",
		moved, movedWorst, readAt.Round(time.Millisecond), bestAt.Round(time.Millisecond), best, bestWorst, cpu)
	if moved < 0.3 {
		t.Errorf("the frame did not move by itself")
	}
	if best > 0.5 || bestWorst > 4 {
		t.Errorf("not the cores' colors: mean %.2f, largest %d", best, bestWorst)
	}
}

// childCPU is the processor time, in seconds, the processes this one started have run so far
// (/proc/<pid>/stat's utime and stime, in hundredths).
func childCPU() float64 {
	var total float64
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		b, err := os.ReadFile("/proc/" + d.Name() + "/stat")
		if err != nil {
			continue
		}
		_, rest, ok := strings.Cut(string(b), ") ")
		f := strings.Fields(rest)
		if !ok || len(f) < 13 {
			continue
		}
		if ppid, _ := strconv.Atoi(f[1]); ppid != os.Getpid() {
			continue
		}
		u, _ := strconv.ParseFloat(f[11], 64)
		s, _ := strconv.ParseFloat(f[12], 64)
		total += (u + s) / 100
	}
	return total
}
