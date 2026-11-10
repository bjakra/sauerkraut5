//go:build linux && !dot && !spot && !panel

package display

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
)

// TestSkyOnGPU draws each of the weather's skies by a real GPU helper, as items over a page, and on the
// cores at the same moment, and compares them; a second on, the GPU's has moved by itself and is
// compared again. The drops' outlines wobble differently on the GPU (a hash of their own, not rnd's),
// so they are not the same pixel for pixel: the check is that the skies are alike, and -out shows them.
//
//	TECHO5_SKY_CHECK=<techo5-gpu> [TECHO5_SKY_OUT=dir] go test -run SkyOnGPU ./internal/feature/display
func TestSkyOnGPU(t *testing.T) {
	helper := os.Getenv("TECHO5_SKY_CHECK")
	if helper == "" {
		t.Skip("TECHO5_SKY_CHECK is not set: a check against a real helper")
	}
	out := os.Getenv("TECHO5_SKY_OUT")
	gpu.SetHelperPath(helper)
	const pw, ph = 480, 960
	cw, ch := ph, pw
	f, err := os.CreateTemp("", "skycheck-fb-*")
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

	page := image.NewRGBA(canvas.Rect)
	for y := range ch {
		for x := range cw {
			o := y*page.Stride + x*4
			r, g, b := 20+x*60/cw, 40+y*80/ch, 70+(x+y)*50/(cw+ch)
			if (y/30)%4 == 0 && x%160 < 110 {
				r, g, b = 220, 200, 170
			}
			page.Pix[o], page.Pix[o+1], page.Pix[o+2], page.Pix[o+3] = uint8(r), uint8(g), uint8(b), 255
		}
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
	diff := func(a, b *image.RGBA) float64 {
		var sum float64
		for i := 0; i < len(a.Pix); i += 4 {
			for c := range 3 {
				d := int(a.Pix[i+c]) - int(b.Pix[i+c])
				if d < 0 {
					d = -d
				}
				sum += float64(d)
			}
		}
		return sum / float64(len(a.Pix)/4*3)
	}
	save := func(name string, img *image.RGBA) {
		if out == "" {
			return
		}
		f, err := os.Create(filepath.Join(out, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, img)
		f.Close()
	}
	r := newRenderer(image.NewRGBA(canvas.Rect))
	var g skyGPU
	frame, err := g.program(skyFrameSlot)
	if err != nil {
		t.Fatal(err)
	}
	// A moment where a strike is lighting the sky (strikes 1.2 s slots), and drops are running.
	t0 := time.Unix(1_700_000_000, 0)
	for s := int(float64(t0.Unix()) / strikeSlot); ; s++ {
		if strikes(s) && rnd(s, 103) >= 0.3 {
			t0 = time.UnixMilli(int64((float64(s) + rnd(s, 102)*0.6) * strikeSlot * 1000)).Add(30 * time.Millisecond)
			break
		}
	}
	strike := image.Rect(cw/2-r.s(80), r.s(70), cw/2-r.s(10), r.s(400))
	for _, fx := range []skyFx{fxRain, fxPour, fxSnow, fxSleet, fxHail, fxStorm, fxFog, fxWind} {
		name := []string{"", "rain", "pour", "snow", "sleet", "hail", "storm", "fog", "wind"}[fx]
		sh := skyShown{drawn: true, fx: fx, at: t0, area: canvas.Rect, strike: strike}
		items, err := g.items(&r.paint, sh)
		if err != nil {
			t.Fatal(err)
		}
		copy(canvas.Pix, page.Pix)
		began := time.Now()
		if err := disp.PresentFrameOf(canvas, &gpu.Frame{Program: frame, Items: items, Every: skyEvery, Lasts: skyLasts}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, readAt := shown(), time.Since(began)
		// The frame on show was drawn at some moment since the present; the cores' sky that is most like
		// it, from moments 10 ms apart, says how alike they are.
		cores, best := image.NewRGBA(canvas.Rect), image.NewRGBA(canvas.Rect)
		alike := 1e9
		for at := time.Duration(0); at <= readAt+skyEvery; at += 10 * time.Millisecond {
			copy(cores.Pix, page.Pix)
			r.dst = cores
			r.sky(fx, t0.Add(at), cores.Rect, strike)
			if d := diff(got, cores); d < alike {
				alike = d
				copy(best.Pix, cores.Pix)
			}
		}
		cores = best
		sky := diff(cores, page)
		save(name+"-gpu", got)
		save(name+"-cores", cores)
		c0, w0 := childCPU(), time.Now()
		time.Sleep(700 * time.Millisecond)
		later := shown()
		cpu := (childCPU() - c0) / time.Since(w0).Seconds() * 100
		moved := diff(later, got)
		fmt.Printf("sky %-6s the cores' sky changes the page by %.2f; the GPU's differs from it by %.2f; moved by itself %.2f in %v; the helper %.0f%% of a core\n",
			name, sky, alike, moved, time.Since(began).Round(time.Millisecond), cpu)
		if alike > sky*0.9+0.6 {
			t.Errorf("%s: the GPU's sky is not the cores' (%.2f, the sky itself %.2f)", name, alike, sky)
		}
		if moved < 0.05 && fx != fxFog {
			t.Errorf("%s: did not move by itself", name)
		}
	}
}
