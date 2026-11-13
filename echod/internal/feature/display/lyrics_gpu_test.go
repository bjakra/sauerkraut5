//go:build linux && !dot && !spot && !panel && !sk5_no_lyrics

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

	"github.com/HuskerMinion/techo5/echod/internal/feature/home"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/screen"
)

// TestLyricsOnGPU moves a song's words on by a real GPU helper, on the classic page and the modern one:
// before the next line is due they are the cores' words to the pixel, once it has moved in they are the
// cores' words for the next line, and in between they glide (-out saves that frame).
//
//	TECHO5_LYRICS_CHECK=<techo5-gpu> [TECHO5_LYRICS_OUT=dir] go test -run LyricsOnGPU ./internal/feature/display
func TestLyricsOnGPU(t *testing.T) {
	helper := os.Getenv("TECHO5_LYRICS_CHECK")
	if helper == "" {
		t.Skip("TECHO5_LYRICS_CHECK is not set: a check against a real helper")
	}
	out := os.Getenv("TECHO5_LYRICS_OUT")
	gpu.SetHelperPath(helper)
	const pw, ph = 480, 960
	cw, ch := ph, pw
	f, err := os.CreateTemp("", "lyricscheck-fb-*")
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
	worst := func(a, b *image.RGBA) (int, int) {
		w, n := 0, 0
		for i := 0; i < len(a.Pix); i += 4 {
			for c := range 3 {
				d := int(a.Pix[i+c]) - int(b.Pix[i+c])
				if d < 0 {
					d = -d
				}
				if d > 2 {
					n++
				}
				w = max(w, d)
			}
		}
		return w, n
	}
	save := func(name string, img *image.RGBA) {
		if out == "" {
			return
		}
		f, _ := os.Create(filepath.Join(out, name+".png"))
		png.Encode(f, img)
		f.Close()
	}
	page := image.NewRGBA(canvas.Rect)
	for i := 0; i < len(page.Pix); i += 4 {
		page.Pix[i], page.Pix[i+1], page.Pix[i+2], page.Pix[i+3] = 34, 30, 28, 255
	}
	r := newRenderer(image.NewRGBA(canvas.Rect))
	d := &Display{r: r}
	l := home.Lyric{Line: "I will follow you into the dark", Next: "If heaven and hell decide that they both are satisfied", After: "Illuminate the no's on their vacancy signs"}
	for _, modern := range []bool{false, true} {
		name := map[bool]string{false: "classic", true: "modern"}[modern]
		l.In = 900 * time.Millisecond
		sh := lyricShown{drawn: true, modern: modern, lyric: l, at: time.Now()}
		// The frame as the display presents it, through a stand-in for the screen.
		copy(canvas.Pix, page.Pix)
		d.sk5().lyr = lyricGPU{}
		err := d.lyricEffectOn(sh, func(e *screen.Effect) error {
			return disp.PresentFrameOf(canvas, &gpu.Frame{Program: e.Program, Items: e.Items, Every: e.Every, Lasts: e.Lasts})
		})
		if err != nil {
			t.Fatal(err)
		}
		before := shown()
		cores := image.NewRGBA(canvas.Rect)
		copy(cores.Pix, page.Pix)
		r.lyricOnCores(sh, cores)
		if w, n := worst(before, cores); w > 3 {
			t.Errorf("%s, before: not the cores' words (%d channels off by more than 2, largest %d)", name, n, w)
		}
		time.Sleep(time.Until(sh.at.Add(l.In - lyricLead + lyricGlide/2)))
		mid := shown()
		time.Sleep(time.Until(sh.at.Add(l.In - lyricLead + lyricGlide + 150*time.Millisecond)))
		after := shown()
		copy(cores.Pix, page.Pix)
		r.lyricOnCores(lyricShown{modern: modern, lyric: home.Lyric{Line: l.Next, Next: l.After}}, cores)
		w, n := worst(after, cores)
		mw, _ := worst(mid, before)
		fmt.Printf("lyrics %-7s before: as the cores; mid-glide differs from before by up to %d; after: %d channels off by more than 2, largest %d\n", name, mw, n, w)
		if w > 3 {
			t.Errorf("%s, after: not the cores' words for the next line", name)
		}
		if mw < 40 {
			t.Errorf("%s: nothing moved mid-glide", name)
		}
		save(name+"-before", before)
		save(name+"-mid", mid)
		save(name+"-after", after)
	}
}
