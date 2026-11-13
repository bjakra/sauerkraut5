//go:build !dot && !spot && !panel && sk5_no_glow

package display

import "time"

// Built without the Glow moved on by the GPU (sk5.go): it moves once a second, drawn by the cores.

type glowR struct{}
type glowGPU struct{}

func (r *renderer) glowDrawn(at time.Time)  {}
func (d *Display) presentGlow(s scene) bool { return false }
