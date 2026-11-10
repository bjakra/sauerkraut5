//go:build !dot && !panel

package screen

import (
	"errors"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
)

// Effect is a frame for the GPU to draw through a shader of the caller's over the canvas (gpu's
// effects): the canvas holds what is under the effect, the page, and the shader lays the rest over it
// for every pixel of the panel, where the cores would have drawn it into the canvas.
type Effect struct {
	Program *gpu.Program
	Params  []float32      // four to a vec4, from p[0]
	Reads   []*gpu.Texture // the textures the program reads
	Passes  []gpu.Pass     // drawn first, into textures it reads
	// Every, if not 0, has the GPU draw the frame again that often by itself, for up to Lasts or until
	// the next frame: an effect that moves by its programs' `since` (gpu.PresentMoving).
	Every, Lasts time.Duration
	// Same says the canvas is as it was at the last frame presented, of either kind: the GPU keeps the
	// copy it has.
	Same bool
}

// ErrNoEffect is a frame that cannot be an effect now.
var ErrNoEffect = errors.New("screen: no effects now")

// CanEffect is whether the next frame may be an Effect: the GPU draws the panel and its helper knows
// effects, no skin's filter is laid over the canvas on its way out, and the lift is at rest (it
// measures the canvas to know how far the backlight may go down, and an effect adds light it would
// not have counted).
func (d *Device) CanEffect() bool {
	return d.gpu != nil && !d.gpuPath.noEffects && d.filter.f == nil && lit.resting()
}

// PresentEffect shows the canvas through e, in place of Present. On an error nothing of the frame was
// shown: the caller draws it whole and presents that. A helper that knows no effects is such an
// error, once, and CanEffect is false from then.
func (d *Device) PresentEffect(e *Effect) error {
	err := d.settle()
	if !d.CanEffect() {
		return ErrNoEffect
	}
	if lit.lost() {
		d.leaveGPU(errors.New("screen: the lift found the GPU helper gone"))
		return ErrNoEffect
	}
	began := time.Now()
	d.gpuPath.ahead = true
	if e := d.gpu.PresentMoving(d.canvas, e.Program, e.Params, e.Reads, e.Same, true, e.Every, e.Lasts, e.Passes...); e != nil {
		var refused *gpu.ProgramError
		switch {
		case errors.Is(e, gpu.ErrNoEffects):
			d.gpuPath.noEffects = true // the helper is fine, and draws plain frames
		case errors.As(e, &refused):
		default:
			d.leaveGPU(e)
		}
		return e
	}
	lit.shown(false, began)
	d.frames.add(time.Since(began), true)
	return err
}
