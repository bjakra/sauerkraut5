//go:build !dot && !spot && !panel && !sk5_no_rainbow

package display

import (
	"errors"
	"image"
	"log/slog"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/screen"
	"github.com/HuskerMinion/techo5/echod/internal/lib/rainbow"
	"github.com/HuskerMinion/techo5/echod/internal/lib/rainbow/ongpu"
)

// The Rainbow drawn by the GPU, where the screen is (sk5.go): the frame's page alone in the canvas, and
// the turn laid over it by lib/rainbow's shaders as the canvas goes to the panel, in place of the cores
// drawing it into the canvas. The entry stays on the cores, and so does any frame the GPU does not take:
// it is drawn on the cores after all and shown the usual way. A shader the GPU's compiler refuses is not
// offered again.

// rainbowR is a renderer's: whether the canvas holds the kept page as the last frame left it (shown
// through the GPU, and nothing drawn in it since) and whether this frame found it so, whether this
// frame's turn went to the GPU, whether its page is the one the GPU has, and what the GPU is given.
type rainbowR struct {
	canvas, canvasKept, onGPU, same bool
	frame                           rainbow.GPUFrame
}

// begin is a frame's start: the canvas is the kept page only until the frame draws anything.
func (x *rainbowR) begin() {
	x.canvasKept, x.canvas, x.onGPU, x.same = x.canvas, false, false, false
}

// rainbowGPU is what the GPU keeps of the Rainbow.
type rainbowGPU struct{ drawer ongpu.Drawer }

// rainbowCanvasKept is whether the canvas still holds the kept page, so it need not be copied back
// (render_rainbow.go's rainbowKept); the frame is then the GPU's page again.
func (r *renderer) rainbowCanvasKept() bool {
	x := &r.sk5().rb
	x.same = x.canvasKept
	return x.canvasKept
}

// rainbowFinish draws the turn over the page, or leaves it to the GPU when it may: not under what is
// drawn over the turn after it, the update's dialog and the dots that say the camera or the room is
// being followed.
func (r *renderer) rainbowFinish(s scene, faces rainbow.Faces) image.Rectangle {
	x := r.sk5()
	if x.gpuOK && !r.sk5Refused("rainbow", false) && !s.updateActive && !s.streaming && !s.listening {
		if at, ok := r.rainbow.FinishGPU(r.dst, faces, &x.rb.frame); ok {
			x.rb.onGPU = true
			return at
		}
	}
	return r.rainbow.Finish(r.dst, faces)
}

// presentRainbow shows the frame through the GPU if its turn was left to it, and says whether it did.
// When it did not, the turn is drawn into the canvas on the cores, to be presented the usual way.
func (d *Display) presentRainbow() bool {
	r := d.r
	x := &r.sk5().rb
	if !x.onGPU {
		return false
	}
	x.onGPU = false
	err := d.rainbowEffect()
	if err == nil {
		// The canvas is the kept page while nothing more is drawn in it.
		x.canvas = r.rainbowPageOK
		return true
	}
	var refused *gpu.ProgramError
	switch {
	case errors.As(err, &refused):
		r.sk5Refused("rainbow", true)
		slog.Warn("the GPU refused the Rainbow's shader: drawn on the cores from now", "err", err)
	case !errors.Is(err, screen.ErrNoEffect):
		slog.Debug("the Rainbow's frame not drawn by the GPU", "err", err)
	}
	r.rainbow.PaintGPU(r.dst)
	return false
}

func (d *Display) rainbowEffect() error {
	g, x := &d.sk5().rb, &d.r.sk5().rb
	prog, params, reads, passes, err := g.drawer.Frame(&x.frame)
	if err != nil {
		return err
	}
	err = d.dev.PresentEffect(&screen.Effect{Program: prog, Params: params, Reads: reads, Passes: passes, Same: x.same})
	if err == nil {
		g.drawer.Shown()
	}
	return err
}

// shotRainbow lays the turn the GPU drew over shot, a copy of the canvas.
func (d *Display) shotRainbow(shot *image.RGBA) { d.r.rainbow.PaintGPU(shot) }
