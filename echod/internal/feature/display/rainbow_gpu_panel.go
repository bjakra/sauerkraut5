//go:build panel && !dot

package display

import "image"

// No Rainbow on a tablet (rainbow_here_panel.go), and so none drawn by a GPU.
type rainbowGPU struct{}

func (d *Display) rainbowGPUOK() bool            { return false }
func (d *Display) presentRainbow() bool          { return false }
func (d *Display) shownRainbow(shot *image.RGBA) {}
