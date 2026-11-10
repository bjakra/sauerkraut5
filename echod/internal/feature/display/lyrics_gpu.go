//go:build !dot && !spot && !panel

package display

import (
	_ "embed"
	"errors"
	"image"
	"log/slog"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/home"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/screen"
)

// The words of a song moving on smoothly, where the GPU draws the screen. On the cores the line being
// sung is drawn again at once when the next one comes, and the page jumps to it. Here each line of the
// words is a picture of its own, cut from one texture, laid where the page draws it, and when the next
// line comes the GPU moves them on by itself: the line sung rises and fades, the next one rises into
// its place and grows into the large line it becomes, and the one after it fades in below. echod draws
// the page again once they are where they go.

//go:embed shaders/lyric.glsl
var lyricGLSL string

// The helper's slots for the words: after the weather's.
const (
	lyricItemsSlot = 11
	lyricFrameSlot = 12
	lyricTexSlot   = 8
	lyricSet       = 0 // items' set: never with the weather's sky in one frame
)

// lyricGlide is how long the words take to move on, and lyricLead how long that starts before the next
// line is due, so that it arrives as it is sung.
const (
	lyricGlide = 360 * time.Millisecond
	lyricLead  = 120 * time.Millisecond
)

// lyricGPU is what the GPU keeps of the words: the programs, the texture of their lines and what it
// holds, the items, and whether the frame last presented had its words drawn by the GPU.
type lyricGPU struct {
	items, frame *gpu.Program
	tex          *gpu.Texture
	set          *gpu.Items
	key          string
	strips       []lyricStrip
	refused      bool
	shown        bool
}

// lyricStrip is a line of the words as a picture: where it is on the screen at rest, its first row in
// the texture, and its first baseline.
type lyricStrip struct {
	at       image.Rectangle
	row      int
	baseline int
}

func (d *Display) lyricGPUOK() bool {
	return d.dev != nil && !d.lgpu.refused && d.dev.CanEffect()
}

// presentLyrics shows the frame with its words drawn by the GPU, if it left them, and says whether it
// did; when it did not, they are drawn on the cores, for the frame to be presented the usual way.
func (d *Display) presentLyrics() bool {
	g, sh := &d.lgpu, d.r.lyricShown
	g.shown = false
	if !sh.drawn {
		return false
	}
	err := d.lyricEffect(sh)
	if err == nil {
		g.shown = true
		return true
	}
	var refused *gpu.ProgramError
	switch {
	case errors.As(err, &refused):
		g.refused = true
		slog.Warn("the GPU refused the words' shader: drawn on the cores from now", "err", err)
	case !errors.Is(err, screen.ErrNoEffect):
		slog.Debug("the words not drawn by the GPU", "err", err)
	}
	d.r.lyricOnCores(sh, d.r.dst)
	return false
}

// lyricOnCores draws the words sh as the cores do, onto dst.
func (r *renderer) lyricOnCores(sh lyricShown, dst *image.RGBA) {
	canvas := r.dst
	r.dst = dst
	cur, next := r.lyricPieces(sh.lyric.Line, sh.lyric.Next, sh.modern)
	for _, p := range append(cur, next...) {
		r.text(p.face, p.text, p.x, p.y, p.color)
	}
	r.dst = canvas
}

// shownLyrics lays the words the GPU drew over shot, a copy of the canvas, for a screenshot.
func (d *Display) shownLyrics(shot *image.RGBA) {
	if d.lgpu.shown && shot != nil {
		d.r.lyricOnCores(d.r.lyricShown, shot)
	}
}

// strip draws pieces into a picture of their own, the screen's width and as high as they reach.
func (r *renderer) strip(pieces []lyricPiece) *image.RGBA {
	if len(pieces) == 0 {
		return nil
	}
	top, bottom := 1<<30, -1<<30
	for _, p := range pieces {
		m := p.face.Metrics()
		top = min(top, p.y-m.Ascent.Ceil()-2)
		bottom = max(bottom, p.y+m.Descent.Ceil()+2)
	}
	img := image.NewRGBA(image.Rect(0, top, r.w, bottom))
	canvas := r.dst
	r.dst = img
	for _, p := range pieces {
		r.text(p.face, p.text, p.x, p.y, p.color)
	}
	r.dst = canvas
	return img
}

func (d *Display) lyricEffect(sh lyricShown) error {
	return d.lyricEffectOn(sh, d.dev.PresentEffect)
}

// lyricEffectOn makes the words' frame and has present show it.
func (d *Display) lyricEffectOn(sh lyricShown, present func(*screen.Effect) error) error {
	g, r := &d.lgpu, d.r
	var err error
	if g.items == nil {
		if g.items, err = gpu.NewItemsProgram(lyricItemsSlot, lyricGLSL); err != nil {
			return err
		}
	}
	if g.frame == nil {
		if g.frame, err = gpu.NewProgram(lyricFrameSlot, "vec3 fx(vec2 c) { return page(c); }\n"); err != nil {
			return err
		}
	}
	if g.set == nil {
		if g.set, err = gpu.NewItems(lyricSet, 16); err != nil {
			return err
		}
	}
	l := sh.lyric
	// The next line moves in when it is due within the frame's time, and is not the line after it again.
	moves := l.Next != "" && l.In > 0 && l.In < skyLasts
	key := "c"
	if sh.modern {
		key = "m"
	}
	key += "\x00" + l.Line + "\x00" + l.Next
	if moves {
		key += "\x00" + l.After + "\x00>"
	}
	if key != g.key {
		if err := g.layOut(r, l, sh.modern, moves); err != nil {
			return err
		}
		g.key = key
	}
	tw, th := g.tex.Size()
	g.set.Program, g.set.Params, g.set.Reads, g.set.Count = g.items, []float32{float32(tw), float32(th), 0, 0}, []*gpu.Texture{g.tex}, 0
	put := func(s lyricStrip, h int, before, after, dyBefore, dyAfter float64, when, glide time.Duration) {
		if s.at.Empty() {
			return
		}
		add(g.set, float64(s.at.Min.X), float64(s.at.Min.Y), float64(s.at.Max.X), float64(s.at.Max.Y),
			float64(s.row), float64(s.row+h), before, after, dyBefore, dyAfter, when.Seconds(), glide.Seconds())
	}
	st := g.strips
	if !moves {
		for _, s := range st[:2] {
			put(s, s.at.Dy(), 1, 1, 0, 0, 0, 0)
		}
	} else {
		when := l.In - lyricLead - time.Since(sh.at)
		// How far the next line rises: from its baseline to the one it takes as the line sung.
		rise := float64(st[1].baseline - st[2].baseline)
		put(st[0], st[0].at.Dy(), 1, 0, 0, -rise, when, lyricGlide)
		put(st[1], st[1].at.Dy(), 1, 0, 0, -rise, when, lyricGlide)
		put(st[2], st[2].at.Dy(), 0, 1, rise, 0, when, lyricGlide)
		put(st[3], st[3].at.Dy(), 0, 1, float64(r.s(12)), 0, when, lyricGlide)
	}
	return present(&screen.Effect{Program: g.frame, Items: []*gpu.Items{g.set}, Every: skyEvery, Lasts: skyLasts})
}

// layOut draws the lines into the texture: the line sung and the next as the page has them now, and,
// when the next moves in, it as the line sung and the one after as the next.
func (g *lyricGPU) layOut(r *renderer, l home.Lyric, modern, moves bool) error {
	cur, next := r.lyricPieces(l.Line, l.Next, modern)
	sets := [][]lyricPiece{cur, next}
	if moves {
		cur2, next2 := r.lyricPieces(l.Next, l.After, modern)
		sets = append(sets, cur2, next2)
	}
	g.strips = g.strips[:0]
	var imgs []*image.RGBA
	h := 0
	for _, ps := range sets {
		img := r.strip(ps)
		s := lyricStrip{row: h}
		if img != nil {
			s.at, s.baseline = img.Rect, ps[0].y
			h += img.Rect.Dy()
		}
		g.strips = append(g.strips, s)
		imgs = append(imgs, img)
	}
	h = max(h, 1)
	if g.tex != nil {
		if w, th := g.tex.Size(); w != r.w || th < h {
			g.tex.Close()
			g.tex = nil
		}
	}
	if g.tex == nil {
		var err error
		if g.tex, err = gpu.NewTexture(lyricTexSlot, r.w, max(h, r.s(400)), true); err != nil {
			return err
		}
	}
	clear(g.tex.Pix)
	stride := r.w * 4
	for i, img := range imgs {
		if img == nil {
			continue
		}
		at := g.strips[i].row * stride
		copy(g.tex.Pix[at:at+len(img.Pix)], img.Pix)
	}
	_, th := g.tex.Size()
	g.tex.Touch(0, th)
	return nil
}
