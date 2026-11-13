//go:build !dot && !spot && !panel

package display

import (
	"image"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
)

// sauerkraut5's way into the display (docs/sauerkraut5.md): where the GPU draws the screen, parts of a
// frame that move (the Rainbow's turn, the Glow's colors, the weather's sky, a song's words) are left
// out of the canvas and drawn by the GPU over it, moving by themselves between the frames drawn here.
//
// Everything of it is in files of its own. TECHO5's files carry one call each where it hooks in: the
// frame's start (renderer.draw: sk5Begin), before a frame is drawn (Display.frame: sk5Frame), its
// present (sk5Present), a screenshot (sk5Shot), the pace of the next frame (sk5Next), and the places a
// part is drawn (skyOver, rainbowFinish, rainbowCanvasKept, glowDrawn, lyricsLeft). Its state is kept
// here, by renderer and by Display, not in their structs, so that no hook depends on another having
// gone in: a hook missing is a part drawn by the cores as before. Each part can be built without
// (sk5_no_rainbow, sk5_no_glow, sk5_no_sky, sk5_no_lyrics), its calls then going to the cores.

// sk5Every is how often the GPU draws a frame that moves by itself again, and sk5Lasts how far ahead
// such a frame goes: past the next frame drawn here, a second on.
const (
	sk5Every = 33 * time.Millisecond
	sk5Lasts = 2500 * time.Millisecond
)

// sk5Renderer is a renderer's share: whether this frame may leave parts to the GPU (sk5Frame says, and
// only for the frame it comes before), and each part's own.
type sk5Renderer struct {
	gpuOK   bool
	refused map[string]bool // the parts whose shaders the GPU refused: drawn by the cores from then
	rb      rainbowR
	glow    glowR
	sky     skyR
	lyr     lyricR
}

// sk5Display is a Display's share: each part's GPU state, and which part the frame last presented
// went out through.
type sk5Display struct {
	rb    rainbowGPU
	glow  glowGPU
	sky   skyGPU
	lyr   lyricGPU
	shown string
}

var (
	sk5Mu sync.Mutex
	sk5Rs = map[*renderer]*sk5Renderer{}
	sk5Ds = map[*Display]*sk5Display{}
)

func (r *renderer) sk5() *sk5Renderer {
	sk5Mu.Lock()
	defer sk5Mu.Unlock()
	x := sk5Rs[r]
	if x == nil {
		x = &sk5Renderer{}
		sk5Rs[r] = x
	}
	return x
}

func (d *Display) sk5() *sk5Display {
	sk5Mu.Lock()
	defer sk5Mu.Unlock()
	x := sk5Ds[d]
	if x == nil {
		x = &sk5Display{}
		sk5Ds[d] = x
	}
	return x
}

// sk5Refused is whether part's shaders were refused, and with refuse, says they were.
func (r *renderer) sk5Refused(part string, refuse bool) bool {
	x := r.sk5()
	if refuse {
		if x.refused == nil {
			x.refused = map[string]bool{}
		}
		x.refused[part] = true
	}
	return x.refused[part]
}

// sk5Begin is a frame's start, before anything is drawn: what the last frame left is let go.
func (r *renderer) sk5Begin() {
	x := r.sk5()
	x.rb.begin()
	x.glow, x.sky, x.lyr = glowR{}, skyR{}, lyricR{}
}

// sk5Frame says, before the frame is drawn, whether it may leave parts to the GPU.
func (d *Display) sk5Frame() {
	if d.r == nil {
		return
	}
	x := d.r.sk5()
	x.gpuOK = d.dev != nil && d.dev.CanEffect()
	// What the last frame left goes here too, not only at the frame's start (sk5Begin): no part is shown
	// from a frame before, whichever hooks went in.
	x.glow, x.sky, x.lyr = glowR{}, skyR{}, lyricR{}
}

// sk5Present shows the frame through the GPU if it left parts to it, or by present.
func (d *Display) sk5Present(s scene, present func() error) error {
	if d.r == nil {
		return present()
	}
	x := d.sk5()
	defer func() { d.r.sk5().gpuOK = false }()
	switch {
	case d.presentRainbow():
		x.shown = "rainbow"
	case d.presentSky():
		x.shown = "sky"
	case d.presentLyrics():
		x.shown = "lyrics"
	case d.presentGlow(s):
		x.shown = "glow"
	default:
		x.shown = ""
		return present()
	}
	return nil
}

// sk5Shot lays over shot, a copy of the canvas, what the GPU drew over the frame on the panel.
func (d *Display) sk5Shot(shot *image.RGBA) {
	if d.dev == nil || shot == nil || !d.dev.EffectShown() {
		return
	}
	switch d.sk5().shown {
	case "rainbow":
		d.shotRainbow(shot)
	case "sky":
		d.shotSky(shot)
	case "lyrics":
		d.shotLyrics(shot)
	}
}

// sk5Next is when the next frame is wanted, when the GPU moves this one on by itself: a part moving
// on the GPU needs no frame from here to move.
func (d *Display) sk5Next(s scene, now time.Time) (time.Duration, bool) {
	if d.dev == nil || !d.dev.EffectShown() {
		return 0, false
	}
	switch d.sk5().shown {
	case "sky":
		return time.Until(now.Truncate(idleFrame).Add(idleFrame)), true
	case "lyrics":
		return d.lyricsNext(s, now)
	}
	return 0, false
}

// skyOver is the weather's sky for a frame: left to the GPU when it may be (weather_gpu.go), else
// drawn by the cores now.
func (r *renderer) skyOver(fx skyFx, t time.Time, area, strike image.Rectangle) {
	if fx == fxNone || !r.skyLeave(fx, t, area, strike) {
		r.sky(fx, t, area, strike)
	}
}

// sk5Item puts an item's twelve numbers into it, if there is room.
func sk5Item(it *gpu.Items, v ...float64) {
	if (it.Count+1)*12 > len(it.Data) {
		return
	}
	o := it.Data[it.Count*12 : it.Count*12+12]
	for i := range o {
		o[i] = 0
		if i < len(v) {
			o[i] = float32(v[i])
		}
	}
	it.Count++
}
