package rainbow

import (
	_ "embed"
	"image"
	"math"
	"sync/atomic"
	"time"
)

// The Rainbow drawn by a GPU: the page alone is drawn on the cores, and a fragment shader (Shader) lays
// the light round the edge, the rounded corners and the card over it at every pixel of the panel, where
// Finish would have drawn them into the page on the cores at a quarter and half of the screen's size.
// The shader reads what does not change per pixel from pictures made here: where each pixel is along
// the edge, worked out once per screen size; the frame's light along the edge, a small table; the
// card's glass, made again only when the page under it changes; and the words, once per change. The
// entry stays on the cores: it is a second and a half of a turn, from copies of the page made as it
// begins.
//
// The shader is GLSL ES 1.00 for a helper that wraps it (hardware/gpu's effects): it defines
// vec3 fx(vec2 c), the color of the canvas's pixel at c, and reads the page with page(c), its numbers
// from p[] and its pictures from t0 to t4.

// GPUParams is how many vec4s of numbers a frame gives the shader; GPUPictures how many pictures it
// reads, t0 on.
const (
	GPUParams   = 14
	GPUPictures = 5
)

// Picture is a picture the shader reads: W by H pixels of R, G, B, A bytes, the first row the top one,
// read smoothly between its pixels when Smooth. Gen changes whenever Pix does, and is 0 for a picture
// not made yet.
type Picture struct {
	W, H   int
	Smooth bool
	Pix    []byte
	Gen    uint64
}

// GPUFrame is a frame for a GPU: the shader's numbers, four to a vec4, and its pictures, by sampler.
type GPUFrame struct {
	Params   [GPUParams * 4]float32
	Pictures [GPUPictures]Picture
}

// gens hands out picture generations.
var gens atomic.Uint64

func nextGen() uint64 { return gens.Add(1) }

// gpuPainted is what FinishGPU left for PaintGPU: the frame drawn on the cores after all.
type gpuPainted struct {
	g             *geometry
	now           time.Time
	glowing, card bool
	corners       float64
}

// gpuState is the Screen's for a GPU: the edge's table, the glass's cells and which words are in
// which of the two pictures kept for them.
type gpuState struct {
	edge     []byte
	frost    []byte
	frostGen uint64 // c.frostGen that frost was made from
	frostPic uint64 // its generation as a picture
	words    [2]*cardWords
	painted  gpuPainted
}

// FinishGPU is Finish for a GPU, which then draws the turn over the page: dst keeps the page as it is,
// and out says the rest. It says where the card is, as Finish does. It is false, with nothing done,
// for a frame Finish has to draw on the cores: the entry's, and those before the edge's geometry for a
// GPU has been worked out (in the background, the first time it is asked for). PaintGPU draws the frame
// on the cores after all, for when the GPU does not take it.
func (s *Screen) FinishGPU(dst *image.RGBA, f Faces, out *GPUFrame) (image.Rectangle, bool) {
	fr := s.frame
	if fr.g == nil {
		return image.Rectangle{}, false
	}
	g := fr.g
	geo, geoGen := g.gpuGeometry() // asked for through the entry, to be ready after it
	if geo == nil || fr.capture || fr.entering {
		return image.Rectangle{}, false
	}
	s.frame = frame{g: g}
	c := &s.card
	cr := c.update(g, fr.now, fr.dt, fr.t, fr.on && s.vis > 0.6 && fr.a >= cardFrom, f)
	if !cr.Empty() {
		c.frost(dst, g, cr)
	}
	corners := min(1, 5*s.vis)
	st := &s.gpu
	st.painted = gpuPainted{g: g, now: fr.now, glowing: fr.glowing, corners: corners, card: !cr.Empty()}

	p := out.Params[:]
	clear(p)
	k := float32(g.k)
	p[0], p[1], p[2] = k, float32(g.wd), float32(g.hd)
	out.Pictures[0] = Picture{W: g.hw, H: g.hh, Pix: geo, Gen: geoGen}
	p[12], p[13] = float32(g.hw), float32(g.hh)
	if fr.glowing {
		b := &s.glowBufs
		fr := b.fr
		p[4], p[5], p[6], p[7] = fr.tm, fr.boost, fr.amp, fr.gain
		p[8], p[9], p[10] = fr.vis, fr.cell, fr.rimA/255
		out.Pictures[1] = Picture{W: along, H: 2, Pix: s.edgeTable(), Gen: nextGen()}
	}
	p[11] = float32(corners)
	if !cr.Empty() {
		s.cardGPU(g, fr.now, p, out)
	} else {
		c.at = image.Rectangle{}
	}
	return c.at, true
}

// edgeTable is the frame's light along the edge as a picture along across, two rows: the color, and
// how much blob there is, in eighths, as sixteen bits in red and green.
func (s *Screen) edgeTable() []byte {
	b, st := &s.glowBufs, &s.gpu
	if len(st.edge) != along*2*4 {
		st.edge = make([]byte, along*2*4)
	}
	e := st.edge
	for i := range along {
		c := &b.color8[i]
		e[i*4], e[i*4+1], e[i*4+2], e[i*4+3] = uint8(c[0]), uint8(c[1]), uint8(c[2]), 255
		a := uint32(min(b.amount[i]/8*65535+0.5, 65535))
		o := (along + i) * 4
		e[o], e[o+1], e[o+2], e[o+3] = uint8(a>>8), uint8(a), 0, 255
	}
	return e
}

// cardGPU fills in the card for the frame in hand, as draw and paint lay it over the page: its box, the
// glass, the words and the thinking dots.
func (s *Screen) cardGPU(g *geometry, now time.Time, p []float32, out *GPUFrame) {
	c, st := &s.card, &s.gpu
	k := g.k
	x0, y0, x1, y1 := c.box(g)
	c.at = image.Rect(int(x0), int(y0), int(math.Ceil(x1)), int(math.Ceil(y1)))
	// What draw would paint the card's picture from: drawn on the cores again in full, whatever it last
	// painted.
	c.look = cardLook{}
	ccx, ccy := (x0+x1)/2, (y0+y1)/2
	scale := c.scale
	p[16], p[17], p[18], p[19] = float32(ccx), float32(ccy), float32((x1-x0)/2), float32((y1-y0)/2)
	p[20], p[21], p[22], p[23] = float32(cardRadius*k*scale), float32(k*scale), float32(c.alpha), float32(1/scale)
	exact := float32(0)
	if scale == 1 {
		exact = 1
	}
	p[24], p[25], p[26], p[27] = float32(y0), float32((y1-y0)/2), float32(c.h*k), exact

	// The glass: the frost's cells, brightened and tinted as makeGlass does, read smoothly.
	if st.frostGen != c.frostGen || st.frostPic == 0 {
		n := len(c.frostImg)
		if cap(st.frost) < n {
			st.frost = make([]byte, n)
		}
		st.frost = st.frost[:n]
		var ink [3]uint32
		for i := range 3 {
			ink[i] = uint32(glassInk[i]*0.42 + 0.5)
		}
		for i := 0; i < n; i += 4 {
			for ch := range 3 {
				st.frost[i+ch] = uint8(min(255, uint32(c.frostImg[i+ch])*294>>8)*148>>8 + ink[ch])
			}
			st.frost[i+3] = 255
		}
		st.frostGen, st.frostPic = c.frostGen, nextGen()
	}
	out.Pictures[2] = Picture{W: c.fw, H: c.fh, Smooth: true, Pix: st.frost, Gen: st.frostPic}
	fa := c.frostAt
	p[28], p[29], p[30], p[31] = float32(fa.Min.X), float32(fa.Min.Y), float32(4*c.fw), float32(4*c.fh)

	// The words: the old going up and out, the new coming up in after it, as paint has them.
	type layer struct {
		cw        *cardWords
		alpha, dy float64
	}
	var layers []layer
	if c.prev != nil {
		so := smooth(0, 1, clamp01(now.Sub(c.swapAt).Seconds()/0.18))
		sw := smooth(0, 1, clamp01((now.Sub(c.swapAt).Seconds()-0.1)/0.3))
		layers = append(layers, layer{c.prev, 1 - so, -8 * so * k}, layer{c.cur, sw, 8 * (1 - sw) * k})
		if sw >= 1 {
			c.prev = nil
		}
	} else if c.cur != nil {
		layers = append(layers, layer{c.cur, 1, 0})
	}
	for n, l := range layers {
		var other *cardWords
		if len(layers) == 2 {
			other = layers[1-n].cw
		}
		slot := st.wordsSlot(l.cw, other)
		img := l.cw.img
		out.Pictures[3+slot] = Picture{W: img.Rect.Dx(), H: img.Rect.Dy(), Smooth: true, Pix: img.Pix, Gen: l.cw.gen}
		at := 32 + 4*n
		p[at], p[at+1], p[at+2], p[at+3] = float32(l.alpha), float32(l.dy), float32(img.Rect.Dx()), float32(img.Rect.Dy())
		p[40+n] = float32(slot)
	}

	// Three dots pulsing one after another at the right of the first line, as draw lays them.
	if c.thinking && c.cur != nil && !c.cur.reply {
		t := float64(now.UnixMilli()%(1<<30)) / 1000
		var dots [3][4]float64
		for i := range 3 {
			ph := 0.5 + 0.5*math.Sin(t*6-float64(i)*0.9)
			lx := (cardW - cardDots + 18 + 11*float64(i)) * k
			ly := 32 * k
			dots[i] = [4]float64{ccx + (lx-cardW*k/2)*scale, ccy + (ly-c.h*k/2)*scale, (3 + ph) * k * scale, (0.25 + 0.6*ph) * c.alpha}
		}
		for i, d := range dots {
			at := 44 + 4*i
			p[at], p[at+1], p[at+2], p[at+3] = float32(d[0]), float32(d[1]), float32(d[2]), float32(d[3])
		}
	}
}

// wordsSlot is which of the two pictures kept for the words holds cw: the one it is in, or the one
// other, the frame's other words, is not in.
func (st *gpuState) wordsSlot(cw, other *cardWords) int {
	for i, w := range st.words {
		if w == cw {
			return i
		}
	}
	i := 0
	if other != nil && st.words[0] == other {
		i = 1
	}
	st.words[i] = cw
	return i
}

// PaintGPU draws on the cores, over the page on dst, the frame FinishGPU gave a GPU: for when the GPU
// did not take it, or for a copy of the screen as it was shown.
func (s *Screen) PaintGPU(dst *image.RGBA) {
	pt := s.gpu.painted
	if pt.g == nil {
		return
	}
	if pt.glowing {
		s.glowEnd(dst, pt.g)
	}
	corners(dst, pt.g, pt.corners)
	if pt.card {
		s.card.draw(dst, pt.g, pt.now)
	}
}

// gpuGeometry is the edge's geometry for the shader, a pixel to each 2 by 2 of the screen's (the half
// grid): where along the edge it is, as sixteen bits in red and green, and how much it belongs to the
// top and bottom (blue) and to the right (alpha); its generation with it. Worked out on one core in
// the background the first time it is asked for, so the frames drawn meanwhile have the others; nil
// until then.
func (g *geometry) gpuGeometry() ([]byte, uint64) {
	g.gpuOnce.Do(func() { go g.makeGPUGeometry() })
	if v := g.gpuPix.Load(); v != nil {
		return v.pix, v.gen
	}
	return nil, 0
}

type gpuGeo struct {
	pix []byte
	gen uint64
}

func (g *geometry) makeGPUGeometry() {
	pix := make([]byte, g.hw*g.hh*4)
	for j := range g.hh {
		for i := range g.hw {
			x, y := float64(2*i+1)/g.k, float64(2*j+1)/g.k
			if d := g.per.sdf(x, y); d <= -3 || d >= maxD+3 {
				continue
			}
			u, ny, nx := g.per.at(x, y)
			v := uint32(min(u*65536, 65535))
			o := (j*g.hw + i) * 4
			pix[o], pix[o+1], pix[o+2], pix[o+3] = uint8(v>>8), uint8(v), uint8(ny*255+0.5), uint8(nx*255+0.5)
		}
	}
	g.gpuPix.Store(&gpuGeo{pix, nextGen()})
}

// Shader is the fragment shader that draws a GPUFrame over the page.
//
//go:embed rainbow.glsl
var Shader string
