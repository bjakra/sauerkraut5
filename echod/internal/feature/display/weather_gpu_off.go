//go:build !dot && !spot && !panel && sk5_no_sky

package display

import (
	"image"
	"time"
)

// Built without the weather's sky on the GPU (sk5.go): the cores draw it.

type skyR struct{}
type skyGPU struct{}

func (r *renderer) skyLeave(fx skyFx, t time.Time, area, strike image.Rectangle) bool { return false }
func (d *Display) presentSky() bool                                                   { return false }
func (d *Display) shotSky(shot *image.RGBA)                                           {}
