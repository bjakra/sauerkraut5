//go:build linux

// gpucheck drives techo5-gpu the way echod's screen does, through the gpu package, and checks what
// reaches the panel against the same pictures worked out here: the canvas turned onto the panel, the
// lift, the color matrix, and a video frame with the canvas's controls over it. It also times the
// presents and says how much of the cores they took.
//
// By default the panel is a file standing in for the framebuffer (the helper's TECHO5_GPU_FB_TEST), so
// nothing is shown and echod can go on running, which also makes the timings those of the GPU's work
// and the copy to the page without the wait for the panel. With -panel it is the device's own
// /dev/fb0: stop echod first (touch /run/techo5/hold; killall techo5), or the two draw over each other.
//
//	gpucheck -helper /usr/local/bin/techo5-gpu [-frames 120] [-panel] [-size 480x960]
package main

import (
	"flag"
	"fmt"
	"image"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
)

func main() {
	helper := flag.String("helper", "/usr/local/bin/techo5-gpu", "the helper to check")
	frames := flag.Int("frames", 120, "presents to time")
	onPanel := flag.Bool("panel", false, "draw on /dev/fb0 itself, not on a file standing in for it")
	size := flag.String("size", "480x960", "the stand-in panel's size, WxH (portrait: the canvas is it turned)")
	flag.Parse()
	if err := run(*helper, *frames, *onPanel, *size); err != nil {
		fmt.Fprintln(os.Stderr, "gpucheck:", err)
		os.Exit(1)
	}
}

// panel is where the helper's pixels end up, read back to compare.
type panel struct {
	w, h int
	mem  []byte
	fb   *os.File // /dev/fb0, or nil for the stand-in file (one page)
	line int
}

// fb_var_screeninfo's first fields; the ioctl fills 160 bytes.
type varInfo struct {
	Xres, Yres, XresVirtual, YresVirtual, Xoffset, Yoffset uint32
	_                                                      [136]byte
}

func openPanel(onPanel bool, size string) (*panel, error) {
	if !onPanel {
		ws, hs, ok := strings.Cut(size, "x")
		w, _ := strconv.Atoi(ws)
		h, _ := strconv.Atoi(hs)
		if !ok || w <= 0 || h <= 0 {
			return nil, fmt.Errorf("a size of %q", size)
		}
		f, err := os.CreateTemp("", "gpucheck-fb-*")
		if err != nil {
			return nil, err
		}
		defer os.Remove(f.Name())
		if err := f.Truncate(int64(w * h * 4)); err != nil {
			return nil, err
		}
		mem, err := unix.Mmap(int(f.Fd()), 0, w*h*4, unix.PROT_READ, unix.MAP_SHARED)
		if err != nil {
			return nil, err
		}
		// The helper opens the file by name before it is removed: it is started by the first call.
		os.Setenv("TECHO5_GPU_FB_TEST", fmt.Sprintf("%dx%d:%s", w, h, f.Name()))
		if !gpu.Available() {
			return nil, fmt.Errorf("the helper did not start")
		}
		return &panel{w: w, h: h, mem: mem, line: w * 4}, nil
	}
	f, err := os.OpenFile("/dev/fb0", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	var v varInfo
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), 0x4600, uintptr(unsafe.Pointer(&v))); e != 0 {
		return nil, fmt.Errorf("FBIOGET_VSCREENINFO: %w", e)
	}
	w, h := int(v.Xres), int(v.Yres)
	pages := max(int(v.YresVirtual)/h, 1)
	mem, err := unix.Mmap(int(f.Fd()), 0, w*h*4*pages, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	return &panel{w: w, h: h, mem: mem, fb: f, line: w * 4}, nil
}

// shown is the page on show, bytes B, G, R, A.
func (p *panel) shown() []byte {
	page := 0
	if p.fb != nil {
		var v varInfo
		if _, _, e := unix.Syscall(unix.SYS_IOCTL, p.fb.Fd(), 0x4600, uintptr(unsafe.Pointer(&v))); e == 0 {
			page = int(v.Yoffset) / p.h
		}
	}
	return p.mem[page*p.h*p.line : (page+1)*p.h*p.line]
}

// paint is frame f's picture: no two neighbors alike, and none like the frame before.
func paint(img *image.RGBA, f int) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	for y := range h {
		row := img.Pix[y*img.Stride:]
		for x := range w {
			row[x*4] = uint8(x + f*3)
			row[x*4+1] = uint8(y + f*5)
			row[x*4+2] = uint8((x>>4)*16 + y>>4 + f*7)
			row[x*4+3] = 255
		}
	}
}

// compare counts the panel's pixels that differ from want(px, py), the R, G, B a pixel should have,
// by more than slack, and gives the largest difference.
func compare(p *panel, slack int, want func(px, py int) (r, g, b int)) (bad, worst int) {
	got := p.shown()
	for py := range p.h {
		for px := range p.w {
			i := py*p.line + px*4
			r, g, b := want(px, py)
			d := max(abs(int(got[i+2])-r), abs(int(got[i+1])-g), abs(int(got[i])-b))
			if d > slack {
				bad++
			}
			worst = max(worst, d)
		}
	}
	return bad, worst
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// busy is the jiffies all cores have spent not idle, from /proc/stat.
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

func run(helper string, frames int, onPanel bool, size string) error {
	gpu.SetHelperPath(helper)
	p, err := openPanel(onPanel, size)
	if err != nil {
		return err
	}
	// The canvas is the panel turned a quarter: panel (px, py) shows canvas (py, panelW-1-px).
	cw, ch := p.h, p.w
	disp, canvas, err := gpu.OpenDisplay(cw, ch, true)
	if err != nil {
		return err
	}
	defer disp.Close()
	if got := disp.Panel(); got.X != p.w || got.Y != p.h {
		return fmt.Errorf("the helper's panel is %v, the framebuffer's %dx%d", got, p.w, p.h)
	}
	fmt.Printf("gpu      %s\npanel    %dx%d, canvas %dx%d\n", gpu.Name(), p.w, p.h, cw, ch)
	at := func(px, py int) (r, g, b int) {
		i := (p.w-1-px)*canvas.Stride + py*4
		return int(canvas.Pix[i]), int(canvas.Pix[i+1]), int(canvas.Pix[i+2])
	}
	failed := false
	report := func(what string, bad, worst, slack int) {
		verdict := "ok"
		if bad != 0 {
			verdict, failed = "FAILED", true
		}
		fmt.Printf("%-9s%d pixel(s) off by more than %d, largest difference %d: %s\n", what, bad, slack, worst, verdict)
	}

	// The turn, exactly, on every frame; and what the presents cost.
	times := make([]float64, 0, frames)
	var bad, worst int
	w0, t0 := busy()
	start := time.Now()
	for f := range frames {
		paint(canvas, f)
		a := time.Now()
		if err := disp.Present(nil); err != nil {
			return fmt.Errorf("present %d: %w", f, err)
		}
		times = append(times, float64(time.Since(a).Microseconds())/1000)
		// Every frame is checked off the clock; on the panel itself only some, reading its memory
		// being slow.
		if !onPanel || f%16 == 0 {
			b, w := compare(p, 0, at)
			bad, worst = bad+b, max(worst, w)
		}
	}
	elapsed := time.Since(start)
	w1, t1 := busy()
	report("turn", bad, worst, 0)
	sort.Float64s(times)
	var sum float64
	for _, t := range times {
		sum += t
	}
	fmt.Printf("present  %d frames: mean %.2f ms, median %.2f, 95%% %.2f, worst %.2f\n", frames, sum/float64(frames),
		times[frames/2], times[frames*95/100], times[frames-1])
	if !onPanel {
		// With the checks in between, the cores' share is no measure of the presents.
		_ = elapsed
	} else if t1 > t0 {
		fmt.Printf("cores    %.1f%% of all four busy over the run (%.1f fps asked back to back)\n", 100*(w1-w0)/(t1-t0),
			float64(frames)/elapsed.Seconds())
	}

	// The same ahead, as echod's screen presents when it has no lift to keep in step: the call returns
	// once the helper has its copy of the canvas, and the canvas is drawn on again at once (painted
	// over here before the frame is looked at, which must not show).
	ahead := make([]float64, 0, frames)
	bad, worst = 0, 0
	keep := make([]byte, len(canvas.Pix))
	for f := range frames {
		paint(canvas, f+3)
		a := time.Now()
		if err := disp.PresentAhead(nil); err != nil {
			return fmt.Errorf("present ahead %d: %w", f, err)
		}
		ahead = append(ahead, float64(time.Since(a).Microseconds())/1000)
		copy(keep, canvas.Pix)
		for i := range canvas.Pix {
			canvas.Pix[i] = 0x55
		}
		if err := disp.Sync(); err != nil {
			return fmt.Errorf("sync %d: %w", f, err)
		}
		copy(canvas.Pix, keep)
		if !onPanel || f%16 == 0 {
			b, w := compare(p, 0, at)
			bad, worst = bad+b, max(worst, w)
		}
	}
	report("ahead", bad, worst, 0)
	sort.Float64s(ahead)
	sum = 0
	for _, t := range ahead {
		sum += t
	}
	fmt.Printf("ahead    %d frames: mean %.2f ms, median %.2f, 95%% %.2f, worst %.2f until the canvas is free again\n", frames,
		sum/float64(frames), ahead[frames/2], ahead[frames*95/100], ahead[frames-1])

	// The lift: every pixel gain times as bright, dithered 4x4, as the helper's shader has it.
	const gain = 1.5
	paint(canvas, 1)
	if err := disp.SetLift(gain, false); err != nil {
		return fmt.Errorf("lift: %w", err)
	}
	if err := disp.Present(nil); err != nil {
		return err
	}
	bayer2 := func(x, y float64) float64 { return 2*x + 3*y - 4*x*y }
	bad, worst = compare(p, 0, func(px, py int) (int, int, int) {
		ax, ay := float64(px%4), float64(py%4)
		m := 4*bayer2(math.Mod(ax, 2), math.Mod(ay, 2)) + bayer2(math.Floor(ax/2), math.Floor(ay/2))
		lift := func(k int) int { return int(min(math.Floor(float64(k)*gain+(m+0.5)/16), 255)) }
		r, g, b := at(px, py)
		return lift(r), lift(g), lift(b)
	})
	report("lift", bad, worst, 0)
	if err := disp.SetLift(1, false); err != nil {
		return err
	}

	// The color matrix: a night's tint.
	tint := [9]float64{1, 0, 0, 0, 0.5, 0, 0, 0, 0.25}
	if caps, err := disp.SetColor(gpu.Color{Matrix: &tint}); err != nil {
		fmt.Printf("color    not checked: %v (%+v)\n", err, caps)
	} else {
		if err := disp.Present(nil); err != nil {
			return err
		}
		bad, worst = compare(p, 1, func(px, py int) (int, int, int) {
			r, g, b := at(px, py)
			return r, int(math.Round(float64(g) * 0.5)), int(math.Round(float64(b) * 0.25))
		})
		report("color", bad, worst, 1)
		if _, err := disp.SetColor(gpu.Color{}); err != nil {
			return err
		}
	}

	// A video frame, in the panel's orientation and the framebuffer's byte order, with a band of the
	// canvas (premultiplied, half opaque) over it.
	frame := make([]byte, p.w*p.h*4)
	for y := range p.h {
		for x := range p.w {
			i := (y*p.w + x) * 4
			frame[i], frame[i+1], frame[i+2], frame[i+3] = uint8(x*2), uint8(y), uint8(x+y), 255
		}
	}
	over := image.Rect(cw/4, ch/4, cw/2, ch/2)
	for y := range ch {
		for x := range cw {
			i := y*canvas.Stride + x*4
			canvas.Pix[i], canvas.Pix[i+1], canvas.Pix[i+2], canvas.Pix[i+3] = uint8(x)/2, uint8(y)/2, 40, 128
		}
	}
	if err := disp.PresentFrame(frame, p.w, p.h, gpu.FrameBGRA, over); err != nil {
		return fmt.Errorf("frame: %w", err)
	}
	bad, worst = compare(p, 1, func(px, py int) (int, int, int) {
		i := (py*p.w + px) * 4
		b, g, r := int(frame[i]), int(frame[i+1]), int(frame[i+2])
		cx, cy := py, p.w-1-px
		if !image.Pt(cx, cy).In(over) {
			return r, g, b
		}
		j := cy*canvas.Stride + cx*4
		mix := func(src uint8, dst int) int { return int(math.Round(float64(src) + float64(dst)*(255-128)/255)) }
		return mix(canvas.Pix[j], r), mix(canvas.Pix[j+1], g), mix(canvas.Pix[j+2], b)
	})
	report("frame", bad, worst, 1)

	if failed {
		return fmt.Errorf("the panel does not show what was drawn")
	}
	return nil
}
