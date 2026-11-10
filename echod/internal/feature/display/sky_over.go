//go:build !dot && !spot

package display

import (
	"image"
	"time"
)

// skyShown is the sky a frame left for the GPU to draw (paint.skyOver), and whether it did.
type skyShown struct {
	drawn        bool
	fx           skyFx
	at           time.Time
	area, strike image.Rectangle
}

// skyOver is sky for a frame: drawn by the cores now, or left for the GPU (presentSky) when it may.
func (r *renderer) skyOver(fx skyFx, t time.Time, area, strike image.Rectangle) {
	if r.skyGPUOK && fx != fxNone {
		r.skyShown = skyShown{drawn: true, fx: fx, at: t, area: area, strike: strike}
		return
	}
	r.sky(fx, t, area, strike)
}
