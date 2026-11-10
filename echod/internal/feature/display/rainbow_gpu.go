//go:build !dot && !spot && !panel

package display

import (
	"errors"
	"image"
	"log/slog"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/screen"
	"github.com/HuskerMinion/techo5/echod/internal/lib/rainbow"
)

// The Rainbow drawn by the GPU, where the screen is: the frame's page alone in the canvas, and the
// turn laid over it by lib/rainbow's shader as the canvas goes to the panel (screen.PresentEffect), in
// place of the cores drawing it into the canvas. The entry stays on the cores, and so does any frame
// the GPU does not take: it is drawn on the cores after all and shown the usual way. A shader the GPU's
// compiler refuses is not offered again.

// rainbowSlot is the helper's program slot for the shader, and its textures' first slot.
const rainbowSlot = 0

// rainbowGPU is what the GPU keeps of the Rainbow: the program, its pictures, and whether it has been
// refused.
type rainbowGPU struct {
	prog    *gpu.Program
	pics    gpu.Pictures
	refused bool
	reads   []*gpu.Texture
}

// rainbowGPUOK is whether this frame's turn may be drawn by the GPU.
func (d *Display) rainbowGPUOK() bool {
	return d.dev != nil && !d.rgpu.refused && d.dev.CanEffect()
}

// presentRainbow shows the frame through the GPU if its turn was left to it, and says whether it did.
// When it did not, the turn is drawn into the canvas on the cores, to be presented the usual way.
func (d *Display) presentRainbow() bool {
	r := d.r
	if !r.rainbowOnGPU {
		return false
	}
	r.rainbowOnGPU = false
	err := d.rainbowEffect()
	if err == nil {
		d.rainbowShown = true
		// The canvas is the kept page while nothing more is drawn in it.
		r.rainbowCanvas = r.rainbowPageOK
		return true
	}
	var refused *gpu.ProgramError
	switch {
	case errors.As(err, &refused):
		d.rgpu.refused = true
		slog.Warn("the GPU refused the Rainbow's shader: drawn on the cores from now", "err", err)
	case !errors.Is(err, screen.ErrNoEffect):
		slog.Debug("the Rainbow's frame not drawn by the GPU", "err", err)
	}
	r.rainbow.PaintGPU(r.dst)
	return false
}

func (d *Display) rainbowEffect() error {
	g := &d.rgpu
	if g.prog == nil {
		p, err := gpu.NewProgram(rainbowSlot, rainbow.Shader)
		if err != nil {
			return err
		}
		g.prog, g.pics.First = p, rainbowSlot
	}
	fr := &d.r.rainbowGPU
	g.reads = g.reads[:0]
	for i, p := range fr.Pictures {
		if p.Gen == 0 {
			continue
		}
		t, err := g.pics.Texture(i, p.W, p.H, p.Smooth, p.Pix, p.Gen)
		if err != nil {
			return err
		}
		g.reads = append(g.reads, t)
	}
	return d.dev.PresentEffect(&screen.Effect{Program: g.prog, Params: fr.Params[:], Reads: g.reads, Same: d.r.rainbowSame})
}

// shownRainbow lays the turn the GPU drew over shot, a copy of the canvas, so that a screenshot is what
// the panel shows.
func (d *Display) shownRainbow(shot *image.RGBA) {
	if d.rainbowShown && shot != nil {
		d.r.rainbow.PaintGPU(shot)
	}
}
