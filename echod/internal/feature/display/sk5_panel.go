//go:build panel && !dot

package display

import (
	"image"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/home"
	"github.com/HuskerMinion/techo5/echod/internal/lib/rainbow"
)

// No GPU on a tablet's panel (sk5.go): everything is drawn as TECHO5 draws it.

func (r *renderer) sk5Begin()                                     {}
func (d *Display) sk5Frame()                                      {}
func (d *Display) sk5Present(s scene, present func() error) error { return present() }
func (d *Display) sk5Shot(shot *image.RGBA)                       {}
func (d *Display) sk5Next(s scene, now time.Time) (time.Duration, bool) {
	return 0, false
}
func (r *renderer) skyOver(fx skyFx, t time.Time, area, strike image.Rectangle) {
	r.sky(fx, t, area, strike)
}
func (r *renderer) rainbowFinish(s scene, f rainbow.Faces) image.Rectangle {
	return r.rainbow.Finish(r.dst, f)
}
func (r *renderer) rainbowCanvasKept() bool                   { return false }
func (r *renderer) glowDrawn(at time.Time)                    {}
func (r *renderer) lyricsLeft(l home.Lyric, modern bool) bool { return false }
