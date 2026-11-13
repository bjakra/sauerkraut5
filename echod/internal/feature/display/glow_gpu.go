//go:build !dot && !spot && !panel && !sk5_no_glow

package display

import (
	_ "embed"
	"errors"
	"image/color"
	"log/slog"
	"math"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/screen"
)

// The Glow clock style moving smoothly, where the screen is drawn by the GPU. The cores draw the clock
// page once a second, as they draw any clock, and its colors drift slowly enough for that to look like
// a drift on paper; on the panel the blobs then jump by up to a few tens of pixels each second. So the
// frame goes out as an effect that moves by itself: the GPU works out, between the cores' frames, how
// much the colors have changed since the page was drawn (shaders/glow-move.glsl, at the size of the
// cores' field) and adds that to the page (shaders/glow.glsl), fifteen times a second until the next
// frame, without echod. Where the page covers the colors (the time, the date) the change is added to
// it too: never more than a second's drift of a soft field, a few 255ths.

var (
	//go:embed shaders/glow.glsl
	glowFrameGLSL string
	//go:embed shaders/glow-move.glsl
	glowMoveGLSL string
)

// The helper's slots for the Glow: after the Rainbow's (lib/rainbow/ongpu).
const (
	glowFrameSlot = 3
	glowMoveSlot  = 4
	glowTexSlot   = 7
)

// glowEvery is how often the GPU draws the Glow again between the cores' frames, and glowLasts how long
// it goes on without one (a page that stops being drawn stops moving soon after).
const (
	glowEvery = 66 * time.Millisecond
	glowLasts = 2500 * time.Millisecond
)

// glowShown is what glowGround drew the colors with, for the GPU to move them on from.
type glowShown struct {
	drawn  bool
	at     time.Time
	fw, fh int
	ground color.RGBA
	colors [3]color.RGBA
}

// glowR is a renderer's: the colors this frame drew.
type glowR struct{ shown glowShown }

// glowGPU is what the GPU keeps of the Glow.
type glowGPU struct {
	frame, move *gpu.Program
	tex         *gpu.Texture
	params      [13 * 4]float32
}

// glowDrawn says the frame drew the Glow's colors at the moment at (render_faces.go's glowGround), with
// glowGround's own field and colors: kept in step with it here.
func (r *renderer) glowDrawn(at time.Time) {
	x := r.sk5()
	if !x.gpuOK || r.sk5Refused("glow", false) {
		return
	}
	colors := [3]color.RGBA{amber, ember, lerp(dim, amber, 0.3)}
	x.glow.shown = glowShown{drawn: true, at: at, fw: r.w/glowCell + 1, fh: r.h/glowCell + 1, ground: walnut, colors: colors}
}

// presentGlow shows the frame as a Glow that moves by itself, if the frame is the Glow's colors under a
// page that does not move otherwise, and says whether it did.
func (d *Display) presentGlow(s scene) bool {
	sh := d.r.sk5().glow.shown
	if !sh.drawn || d.dev == nil || turnShown(s) || d.r.rainbow.Active() || !d.dev.CanEffect() {
		return false
	}
	err := d.glowEffect(sh)
	if err == nil {
		return true
	}
	var refused *gpu.ProgramError
	switch {
	case errors.As(err, &refused):
		d.r.sk5Refused("glow", true)
		slog.Warn("the GPU refused the Glow's shaders: it moves once a second from now", "err", err)
	case !errors.Is(err, screen.ErrNoEffect):
		slog.Debug("the Glow's frame not shown by the GPU", "err", err)
	}
	return false
}

func (d *Display) glowEffect(sh glowShown) error {
	prog, params, reads, passes, err := d.sk5().glow.effect(sh)
	if err != nil {
		return err
	}
	return d.dev.PresentEffect(&screen.Effect{Program: prog, Params: params, Reads: reads, Passes: passes, Every: glowEvery, Lasts: glowLasts})
}

// effect is the Glow's frame for the colors sh: its program, numbers, textures and pass.
func (g *glowGPU) effect(sh glowShown) (*gpu.Program, []float32, []*gpu.Texture, []gpu.Pass, error) {
	var err error
	if g.frame == nil {
		if g.frame, err = gpu.NewProgram(glowFrameSlot, glowFrameGLSL); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	if g.move == nil {
		if g.move, err = gpu.NewPassProgram(glowMoveSlot, glowMoveGLSL); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	if g.tex != nil {
		if w, h := g.tex.Size(); w != sh.fw || h != sh.fh {
			g.tex.Close()
			g.tex = nil
		}
	}
	if g.tex == nil {
		if g.tex, err = gpu.NewTexture(glowTexSlot, sh.fw, sh.fh, true); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	p := g.params[:]
	clear(p)
	p[0], p[1] = float32(sh.fw), float32(sh.fh)
	for i, c := range append(sh.colors[:], sh.ground) {
		p[4+4*i], p[5+4*i], p[6+4*i] = float32(c.R)/255, float32(c.G)/255, float32(c.B)/255
	}
	// Each blob's place on its path as glowField works it out at the page's moment, in radians, and its
	// speed along it.
	t := float64(sh.at.UnixMilli()) / 1000
	for i, b := range glowBlobs {
		ax := math.Mod(2*math.Pi*t/b.periodX+b.phase, 2*math.Pi)
		ay := math.Mod(2*math.Pi*t/b.periodY+b.phase*1.3, 2*math.Pi)
		o := 20 + 4*i
		p[o], p[o+1], p[o+2], p[o+3] = float32(ax), float32(ay), float32(2*math.Pi/b.periodX), float32(2*math.Pi/b.periodY)
		r := b.size * float64(sh.fh)
		o = 36 + 4*i
		p[o], p[o+1] = float32(r*r), float32(b.color)
	}
	move := gpu.Pass{Program: g.move, Target: g.tex, Params: p}
	frame := []float32{float32(sh.fw), float32(sh.fh), glowCell, 0}
	return g.frame, frame, []*gpu.Texture{g.tex}, []gpu.Pass{move}, nil
}
