//go:build !dot && !spot && !panel

package display

import (
	"errors"
	"image"
	"log/slog"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/screen"
	"github.com/HuskerMinion/techo5/echod/internal/lib/rainbow/ongpu"
)

// The Rainbow drawn by the GPU, where the screen is: the frame's page alone in the canvas, and the
// turn laid over it by lib/rainbow's shader as the canvas goes to the panel (screen.PresentEffect), in
// place of the cores drawing it into the canvas. The entry stays on the cores, and so does any frame
// the GPU does not take: it is drawn on the cores after all and shown the usual way. A shader the GPU's
// compiler refuses is not offered again.

// rainbowGPU is what the GPU keeps of the Rainbow, and whether its shaders have been refused.
type rainbowGPU struct {
	drawer  ongpu.Drawer
	refused bool
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
	prog, params, reads, passes, err := g.drawer.Frame(&d.r.rainbowGPU)
	if err != nil {
		return err
	}
	err = d.dev.PresentEffect(&screen.Effect{Program: prog, Params: params, Reads: reads, Passes: passes, Same: d.r.rainbowSame})
	if err == nil {
		g.drawer.Shown()
	}
	return err
}

// shownRainbow lays the turn the GPU drew over shot, a copy of the canvas, so that a screenshot is what
// the panel shows.
func (d *Display) shownRainbow(shot *image.RGBA) {
	if d.rainbowShown && shot != nil {
		d.r.rainbow.PaintGPU(shot)
	}
}
