//go:build !dot && !spot && !panel

package display

import (
	_ "embed"
	"errors"
	"image"
	"image/color"
	"log/slog"
	"math"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/screen"
)

// The weather's sky moving smoothly, where the screen is drawn by the GPU. On the cores the page is
// drawn again whole every fxFrame to move the rain, a frame of 25 ms and more, and at that pace a drop
// falling a screen in a second jumps tens of pixels a frame. Here the page is drawn once a second without
// the sky, and the sky goes over it as items (gpu.Items) the GPU moves by itself thirty times a second:
// each drop, flake, band of fog and stretch of lightning one, from the very numbers weatherfx.go draws
// it from (rnd), for the next few seconds; the drops on the glass each a lens over the page.

var (
	//go:embed shaders/sky-streak.glsl
	skyStreakGLSL string
	//go:embed shaders/sky-flake.glsl
	skyFlakeGLSL string
	//go:embed shaders/sky-glass.glsl
	skyGlassGLSL string
	//go:embed shaders/sky-fog.glsl
	skyFogGLSL string
	//go:embed shaders/sky-bolt.glsl
	skyBoltGLSL string
)

// The helper's program slots for the sky, after the Glow's: the items' programs, then the frame's own.
const (
	skyStreakSlot = 5 + iota
	skyFlakeSlot
	skyGlassSlot
	skyFogSlot
	skyBoltSlot
	skyFrameSlot
)

// skyEvery is how often the GPU draws the sky again, and skyLasts how far ahead its items go: past the
// next frame the cores draw, a second on.
const (
	skyEvery = 33 * time.Millisecond
	skyLasts = 2500 * time.Millisecond
)

// skyGPU is what the GPU keeps of the sky: its programs, the items' memory, and whether a shader was
// refused.
type skyGPU struct {
	progs   [skyFrameSlot - skyStreakSlot + 1]*gpu.Program
	sets    [4]*gpu.Items
	refused bool
	shown   bool // the frame last presented had its sky drawn by the GPU
}

func (g *skyGPU) program(slot int) (*gpu.Program, error) {
	i := slot - skyStreakSlot
	if g.progs[i] != nil {
		return g.progs[i], nil
	}
	var err error
	switch slot {
	case skyFrameSlot:
		g.progs[i], err = gpu.NewProgram(slot, "vec3 fx(vec2 c) { return page(c); }\n")
	default:
		src := map[int]string{skyStreakSlot: skyStreakGLSL, skyFlakeSlot: skyFlakeGLSL, skyGlassSlot: skyGlassGLSL,
			skyFogSlot: skyFogGLSL, skyBoltSlot: skyBoltGLSL}[slot]
		g.progs[i], err = gpu.NewItemsProgram(slot, src)
	}
	return g.progs[i], err
}

// set is items' memory in set n, for prog with params.
func (g *skyGPU) set(n, slot int, params []float32) (*gpu.Items, error) {
	if g.sets[n] == nil {
		it, err := gpu.NewItems(n, gpu.MaxItems)
		if err != nil {
			return nil, err
		}
		g.sets[n] = it
	}
	prog, err := g.program(slot)
	if err != nil {
		return nil, err
	}
	it := g.sets[n]
	it.Program, it.Params, it.Count = prog, params, 0
	return it, nil
}

// add puts an item's twelve numbers into it, if there is room.
func add(it *gpu.Items, v ...float64) {
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

func skyColor(c color.RGBA) []float32 {
	return []float32{float32(c.R) / 255, float32(c.G) / 255, float32(c.B) / 255, 1}
}

// presentSky shows the frame with its sky drawn by the GPU, if the frame left it one, and says whether
// it did. When it did not, the sky is drawn on the cores, for the frame to be presented the usual way.
func (d *Display) presentSky() bool {
	g, sh := &d.sgpu, d.r.skyShown
	g.shown = false
	if !sh.drawn {
		return false
	}
	err := d.skyEffect(sh)
	if err == nil {
		g.shown = true
		return true
	}
	var refused *gpu.ProgramError
	switch {
	case errors.As(err, &refused):
		g.refused = true
		slog.Warn("the GPU refused a shader of the weather's: drawn on the cores from now", "err", err)
	case !errors.Is(err, screen.ErrNoEffect):
		slog.Debug("the weather's sky not drawn by the GPU", "err", err)
	}
	d.r.sky(sh.fx, sh.at, sh.area, sh.strike)
	return false
}

// skyGPUOK is whether this frame's sky may be left to the GPU.
func (d *Display) skyGPUOK() bool {
	return d.dev != nil && !d.sgpu.refused && d.dev.CanEffect()
}

func (d *Display) skyEffect(sh skyShown) error {
	items, err := d.sgpu.items(&d.r.paint, sh)
	if err != nil {
		return err
	}
	frame, err := d.sgpu.program(skyFrameSlot)
	if err != nil {
		return err
	}
	return d.dev.PresentEffect(&screen.Effect{Program: frame, Items: items, Every: skyEvery, Lasts: skyLasts})
}

// items are sky sh as the GPU draws it, in weatherfx.go's order and from its numbers.
func (g *skyGPU) items(p *paint, sh skyShown) ([]*gpu.Items, error) {
	secs := float64(sh.at.UnixMilli()) / 1000
	var out []*gpu.Items
	var err error
	streaks := func(c color.RGBA, along [2]float32) *gpu.Items {
		if err != nil {
			return nil
		}
		var it *gpu.Items
		it, err = g.set(0, skyStreakSlot, append(skyColor(c), along[0], along[1], 0, 0))
		if it != nil {
			out = append(out, it)
		}
		return it
	}
	glass := func(n int, alpha float64) {
		if err != nil {
			return
		}
		var it *gpu.Items
		params := []float32{float32(alpha), float32(p.s(1)), float32(p.s(5)), float32(p.s(40)), float32(p.s(14)), 0, 0, 0}
		if it, err = g.set(2, skyGlassSlot, params); it != nil {
			out = append(out, it)
			p.glassItems(it, secs, sh.area, n)
		}
	}
	down := [2]float32{0, 1}
	switch sh.fx {
	case fxRain:
		p.rainItems(streaks(fxRainColor, down), secs, sh.area, 45, 1.0, 0.22)
		glass(48, 0.9)
	case fxPour:
		p.rainItems(streaks(fxRainColor, down), secs, sh.area, 100, 1.35, 0.28)
		glass(75, 0.95)
	case fxSnow:
		g.snowItems(p, &out, &err, secs, sh.area, 45, 0.85)
	case fxSleet:
		p.rainItems(streaks(fxRainColor, down), secs, sh.area, 35, 1.0, 0.24)
		g.snowItems(p, &out, &err, secs, sh.area, 22, 0.7)
		glass(24, 0.8)
	case fxHail:
		p.hailItems(streaks(fxSnowColor, down), secs, sh.area, 40)
	case fxStorm:
		p.rainItems(streaks(fxRainColor, down), secs, sh.area, 80, 1.25, 0.26)
		glass(60, 0.95)
		g.boltItems(p, &out, &err, secs, sh.area, sh.strike)
	case fxFog:
		if err == nil {
			var it *gpu.Items
			if it, err = g.set(3, skyFogSlot, skyColor(fxFogColor)); it != nil {
				out = append(out, it)
				p.fogItems(it, secs, sh.area)
			}
		}
	case fxWind:
		p.windItems(streaks(fxFogColor, [2]float32{1, 0}), secs, sh.area, 14)
	}
	return out, err
}

// rainItems are rain's streaks (weatherfx.go's rain).
func (p *paint) rainItems(it *gpu.Items, secs float64, a image.Rectangle, n int, speed, alpha float64) {
	if it == nil {
		return
	}
	w, h := float64(a.Dx()), float64(a.Dy())
	for i := range n {
		near := rnd(i, 4)
		length := float64(p.s(12)) + float64(p.s(22))*near
		v := h * speed * (0.6 + 0.4*near + 0.3*rnd(i, 1))
		x := float64(a.Min.X) + rnd(i, 0)*w
		at := wrap(rnd(i, 2)*(h+length)+secs*v, h+length)
		width := math.Max(1, float64(p.s(1))*(0.6+0.8*near))
		add(it, x, float64(a.Min.Y), at, v, h+length, length, width, alpha*(0.35+0.65*near)*(0.7+0.3*rnd(i, 3)), length*0.18, 0, 0, length)
	}
}

// hailItems are hail's pellets (weatherfx.go's hail), as streaks of no length.
func (p *paint) hailItems(it *gpu.Items, secs float64, a image.Rectangle, n int) {
	if it == nil {
		return
	}
	w, h := float64(a.Dx()), float64(a.Dy())
	for i := range n {
		r := float64(p.s(2)) + rnd(i, 3)*float64(p.s(1))
		v := h * (0.9 + 0.4*rnd(i, 1))
		x := float64(a.Min.X) + rnd(i, 0)*w
		add(it, x, float64(a.Min.Y), wrap(rnd(i, 2)*(h+2*r)+secs*v, h+2*r), v, h+2*r, r, 2*r, 0.7)
	}
}

// windItems are the wind's streaks (weatherfx.go's wind).
func (p *paint) windItems(it *gpu.Items, secs float64, a image.Rectangle, n int) {
	if it == nil {
		return
	}
	w, h := float64(a.Dx()), float64(a.Dy())
	length := float64(p.s(70))
	for i := range n {
		v := w * (0.5 + 0.4*rnd(i, 1))
		y := float64(a.Min.Y) + rnd(i, 2)*h
		add(it, float64(a.Min.X), y, wrap(rnd(i, 0)*(w+length)+secs*v, w+length), v, w+length, length,
			math.Max(1, float64(p.s(2))*0.6), 0.18, 0, 0, length, -length*0.05)
	}
}

// snowItems are snow's flakes (weatherfx.go's snow).
func (g *skyGPU) snowItems(p *paint, out *[]*gpu.Items, err *error, secs float64, a image.Rectangle, n int, alpha float64) {
	if *err != nil {
		return
	}
	w, h := float64(a.Dx()), float64(a.Dy())
	params := append(skyColor(fxSnowColor), float32(w), float32(a.Min.X), float32(math.Max(1, float64(p.s(2))*0.6)), float32(p.s(6)))
	it, e := g.set(1, skyFlakeSlot, params)
	if e != nil {
		*err = e
		return
	}
	*out = append(*out, it)
	for i := range n {
		r := float64(p.s(4)) + rnd(i, 3)*float64(p.s(5))
		v := h * (0.07 + 0.06*rnd(i, 1)) * (0.7 + 0.3*r/float64(p.s(9)))
		sway := 0.6 + 0.5*rnd(i, 4)
		turn := (rnd(i, 8) - 0.5) * 0.8
		add(it, rnd(i, 0)*w, math.Mod(secs*sway+rnd(i, 5)*6.28, 2*math.Pi), sway, float64(p.s(14)),
			wrap(rnd(i, 2)*(h+2*r)+secs*v, h+2*r), v, h+2*r, r,
			math.Mod(rnd(i, 7)*math.Pi+secs*turn, 2*math.Pi), turn, alpha*(0.6+0.4*rnd(i, 6)), float64(a.Min.Y))
	}
}

// fogItems are the fog's bands (weatherfx.go's fog).
func (p *paint) fogItems(it *gpu.Items, secs float64, a image.Rectangle) {
	h := float64(a.Dy())
	for i := range 3 {
		fi := float64(i)
		add(it, float64(a.Min.Y)+h*(0.25+0.25*fi), math.Mod(secs*0.15+fi*2, 2*math.Pi), float64(p.s(20)), float64(p.s(40)),
			math.Mod(secs*0.25+fi*1.7, 2*math.Pi), math.Mod(secs*0.4+fi, 2*math.Pi), float64(a.Min.X), float64(a.Max.X))
	}
}

// seedFrac is a number under 1 of a drop's own, for its outline's wobble.
func seedFrac(seed int) float64 { return math.Mod(float64(uint32(seed))*0.6180339887, 1) }

// glassItems are the drops on the glass (weatherfx_glass.go's glassRain): each drop's lives that fall in
// the items' time, its trail and beads when it runs, and the mist.
func (p *paint) glassItems(it *gpu.Items, secs float64, a image.Rectangle, n int) {
	if a.Empty() {
		return
	}
	w, h := float64(a.Dx()), float64(a.Dy())
	lasts := skyLasts.Seconds()
	// lives calls fn with each cycle of a life period long, offset into it, that is under way within the
	// items' time: its number and when it starts, from the frame's start.
	lives := func(period, offset float64, fn func(cycle, start float64)) {
		cycle := math.Floor((secs + offset) / period)
		for start := cycle*period - offset - secs; start < lasts; start, cycle = start+period, cycle+1 {
			fn(cycle, start)
		}
	}
	for i := range 3 * n {
		period := 20 + 25*rnd(i, 81)
		lives(period, rnd(i, 82)*period, func(cycle, start float64) {
			seed := i*104729 + int(math.Mod(cycle, 1000003)) + 7
			r := float64(p.s(1)) * (0.9 + 1.6*rnd(seed, 1))
			add(it, float64(a.Min.X)+rnd(seed, 3)*w, float64(a.Min.Y)+rnd(seed, 4)*h, r, start, period, -1, 0, 0, 0, 0, 0, 1+seedFrac(seed))
		})
	}
	for i := range n {
		period := 8 + 10*rnd(i, 61)
		lives(period, rnd(i, 62)*period, func(cycle, start float64) {
			seed := i*7919 + int(math.Mod(cycle, 1000003))
			r0 := float64(p.s(2)) + float64(p.s(13))*rnd(seed, 1)*rnd(seed, 2)
			x0 := float64(a.Min.X) + rnd(seed, 3)*w
			y0 := float64(a.Min.Y) + rnd(seed, 4)*h*0.85
			runs := r0 > float64(p.s(6)) && rnd(seed, 5) < 0.6
			runAt := -1.0
			if runs {
				runAt = 1 + rnd(seed, 6)*(period-7)
			}
			v := h * (0.1 + 0.14*rnd(seed, 7))
			f := 1.2 + 1.3*rnd(seed, 8)
			most := h * (0.12 + 0.5*rnd(seed, 9))
			ph20, ph21 := rnd(seed, 20)*6.3, rnd(seed, 21)*6.3
			if runs {
				add(it, x0, y0, r0, start, period, runAt, v, f, most, ph20, ph21, 3+seedFrac(seed))
				gap := float64(p.s(18))
				for k := 1; float64(k)*gap < most-r0*2; k++ {
					if rnd(seed, 30+k) < 0.75 {
						continue
					}
					d := float64(k)*gap + (rnd(seed, 50+k)-0.5)*gap*0.6
					add(it, x0+p.wander(seed, d), y0+d, r0*(0.22+0.15*rnd(seed, 70+k)), start, period, runAt, v, f, most, d, r0, 2+seedFrac(seed*31+k))
				}
			}
			add(it, x0, y0, r0, start, period, runAt, v, f, most, ph20, ph21, seedFrac(seed))
		})
	}
}

// boltItems are a storm's strikes within the items' time (weatherfx_glass.go's lightning): each one's
// flash, then its bolt's glow, then its channel.
func (g *skyGPU) boltItems(p *paint, out *[]*gpu.Items, err *error, secs float64, a, b image.Rectangle) {
	if *err != nil {
		return
	}
	lasts := skyLasts.Seconds()
	params := make([]float32, 9*4)
	type strike struct {
		slot     int
		at       float64
		cloud    bool
		strength float64
		spread   float64
		mid      float64
	}
	var strikesIn []strike
	for s := int(math.Floor(secs/strikeSlot)) - 2; float64(s)*strikeSlot < secs+lasts && len(strikesIn) < 3; s++ {
		if !strikes(s) {
			continue
		}
		at := (float64(s) + rnd(s, 102)*0.6) * strikeSlot
		if at+1.6 < secs || at > secs+lasts {
			continue
		}
		k := strike{slot: s, at: at - secs, cloud: rnd(s, 103) < 0.3, strength: 0.3, spread: 0.8, mid: float64(b.Min.X+b.Max.X) / 2}
		if k.cloud || b.Empty() {
			k.strength, k.spread, k.mid = 0.42, 0.5, float64(a.Min.X)+rnd(s, 105)*float64(a.Dx())
		}
		j := len(strikesIn)
		n := 2 + int(rnd(s, 104)*3)
		params[12*j], params[12*j+1] = float32(k.at), float32(n)
		at2 := 0.0
		for st := 1; st < n && st <= 3; st++ {
			at2 += 0.05 + 0.14*rnd(s, 110+st)
			params[12*j+4+st-1] = float32(at2)
			params[12*j+8+st-1] = float32(0.5 + 0.5*rnd(s, 120+st))
		}
		strikesIn = append(strikesIn, k)
	}
	if len(strikesIn) == 0 {
		return
	}
	it, e := g.set(3, skyBoltSlot, params)
	if e != nil {
		*err = e
		return
	}
	*out = append(*out, it)
	area := a.Intersect(p.dst.Rect)
	for j, k := range strikesIn {
		add(it, 0, float64(j), k.strength, k.spread*float64(area.Dx()), float64(area.Min.X), float64(area.Min.Y), float64(area.Max.X), float64(area.Max.Y), k.mid)
		if k.cloud || b.Empty() {
			continue
		}
		segs := p.boltSegs(b, k.slot)
		for _, s := range segs {
			add(it, 1, float64(j), s.w, s.a, s.x0, s.y0, s.x1, s.y1)
		}
		for _, s := range segs {
			add(it, 2, float64(j), s.w, s.a, s.x0, s.y0, s.x1, s.y1)
		}
	}
}

// shownSky lays the sky the GPU drew over shot, a copy of the canvas, so that a screenshot is what the
// panel shows.
func (d *Display) shownSky(shot *image.RGBA) {
	if !d.sgpu.shown || shot == nil {
		return
	}
	sh, canvas := d.r.skyShown, d.r.dst
	d.r.dst = shot
	d.r.sky(sh.fx, sh.at, sh.area, sh.strike)
	d.r.dst = canvas
}
