//go:build !dot && !spot && !panel && sk5_no_rainbow

package display

import (
	"image"

	"github.com/HuskerMinion/techo5/echod/internal/lib/rainbow"
)

// Built without the Rainbow on the GPU (sk5.go): the cores draw it.

type rainbowR struct{}

func (x *rainbowR) begin() {}

type rainbowGPU struct{}

func (r *renderer) rainbowCanvasKept() bool { return false }
func (r *renderer) rainbowFinish(s scene, faces rainbow.Faces) image.Rectangle {
	return r.rainbow.Finish(r.dst, faces)
}
func (d *Display) presentRainbow() bool         { return false }
func (d *Display) shotRainbow(shot *image.RGBA) {}
