//go:build panel && !dot

package display

// No Glow drawn by a GPU on a tablet: the app draws its own panel.
type glowGPU struct{}

func (d *Display) presentGlow(s scene) bool { return false }
