//go:build !dot && !spot && !panel && sk5_no_lyrics

package display

import (
	"image"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/home"
)

// Built without a song's words on the GPU (sk5.go): the cores draw them.

type lyricR struct{}
type lyricGPU struct{}

func (r *renderer) lyricsLeft(l home.Lyric, modern bool) bool { return false }
func (d *Display) presentLyrics() bool                        { return false }
func (d *Display) shotLyrics(shot *image.RGBA)                {}
func (d *Display) lyricsNext(s scene, now time.Time) (time.Duration, bool) {
	return 0, false
}
