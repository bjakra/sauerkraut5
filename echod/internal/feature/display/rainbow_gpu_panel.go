//go:build panel && !dot

package display

import "image"

// No Rainbow on a tablet (rainbow_here_panel.go), and so none drawn by a GPU.
type rainbowGPU struct{}

func (d *Display) rainbowGPUOK() bool            { return false }
func (d *Display) presentRainbow() bool          { return false }
func (d *Display) shownRainbow(shot *image.RGBA) {}

// Nor a sky drawn by one (weather_gpu.go).
type skyGPU struct{ shown bool }

func (d *Display) skyGPUOK() bool            { return false }
func (d *Display) presentSky() bool          { return false }
func (d *Display) shownSky(shot *image.RGBA) {}

// Nor words of a song moved on by one (lyrics_gpu.go).
type lyricGPU struct{ shown bool }

const lyricLead, lyricGlide = 0, 0

func (d *Display) lyricGPUOK() bool             { return false }
func (d *Display) presentLyrics() bool          { return false }
func (d *Display) shownLyrics(shot *image.RGBA) {}
