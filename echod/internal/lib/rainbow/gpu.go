package rainbow

import (
	_ "embed"
	"image"
	"math"
	"sync/atomic"
	"time"
)

// The Rainbow drawn by a GPU: the page alone is drawn on the cores, and the GPU lays the light round
// the edge, the rounded corners and the card over it, where Finish would have drawn them into the page
// on the cores. As on the cores, the light is worked out at half the screen's size (light.glsl, a pass
// into a texture of its own each frame) and the card's picture only when it changes (card.glsl, into
// another); the frame's own program (rainbow.glsl) lays both and the thinking dots over the page at
// every pixel. They read what does not change per pixel from pictures made here: where each pixel is
// along the edge, worked out once per screen size; the frame's light along the edge, a small table;
// the card's glass, made again only when the page under it changes; and the words, once per change.
// The entry stays on the cores: it is a second and a half of a turn, from copies of the page made as
// it begins.
//
// The shaders are GLSL ES 1.00 for a helper that wraps them (hardware/gpu's effects): the frame's
// defines vec3 fx(vec2 c), the color of the canvas's pixel at c, and a pass's vec4 pass(vec2 c), its
// texture's pixel at c; they read the page with page(c), their numbers from p[] and their pictures from
// t0 to t6.

// The pictures, by sampler: those made here, and the two the passes draw into.
const (
	picGeometry = iota
	picEdge
	picFrost
	picWords0
	picWords1
	picLight
	picCard

	GPUPictures
)

// The shaders: the frame's, then the passes'.
const (
	ShaderFrame = iota
	ShaderLight
	ShaderCard

	GPUShaders
)

// Shaders are the shaders' sources, by ShaderFrame, ShaderLight and ShaderCard.
var Shaders = [GPUShaders]string{frameGLSL, lightGLSL, cardGLSL}

var (
	//go:embed rainbow.glsl
	frameGLSL string
	//go:embed light.glsl
	lightGLSL string
	//go:embed card.glsl
	cardGLSL string
)

// Picture is a picture a shader reads: W by H pixels of R, G, B, A bytes, the first row the top one,
// read smoothly between its pixels when Smooth. Gen changes whenever Pix does, and is 0 for a picture
// not made yet. A picture a pass draws into has no Pix.
type Picture struct {
	W, H   int
	Smooth bool
	Pix    []byte
	Gen    uint64
}

// GPUPass is a pass of a frame: its shader, drawn into a picture (the part of it in Rect, all of it
// when empty), with its numbers, four to a vec4, reading pictures. Optional is a pass the same as the
// last frame's, needed only if its picture does not have what that drew.
type GPUPass struct {
	Shader   int
	Target   int
	Rect     image.Rectangle
	Params   []float32
	Reads    []int
	Optional bool
}

// GPUFrame is a frame for a GPU: its passes, then the frame's own shader's numbers and the pictures
// it reads; and all the pictures, by sampler.
type GPUFrame struct {
	Passes   []GPUPass
	Params   [6 * 4]float32
	Reads    []int
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

// gpuState is the Screen's for a GPU: the edge's table, the glass's cells, which words are in which
// of the two pictures kept for them, the passes' numbers, and what the card's pass last drew.
type gpuState struct {
	edge         []byte
	frost        []byte
	frostGen     uint64 // c.frostGen that frost was made from
	frostPic     uint64 // its generation as a picture
	words        [2]*cardWords
	light, cardP [8 * 4]float32
	cardWas      [8 * 4]float32
	cardWasPics  [3]uint64
	passes       [2]GPUPass
	reads        [3][]int
	painted      gpuPainted
}

// The card's texture: wide enough for the card at its widest, as high as the screen.
func cardTexture(g *geometry) (w, h int) { return int(math.Ceil(cardW*g.k)) + 2, g.h }

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
	out.Passes, out.Reads = out.Passes[:0], out.Reads[:0]
	out.Pictures[picGeometry] = Picture{W: g.hw, H: g.hh, Pix: geo, Gen: geoGen}
	out.Pictures[picLight] = Picture{W: g.hw + 2, H: g.hh + 2, Smooth: true, Gen: 1}
	if fr.glowing {
		b := &s.glowBufs
		lf := b.fr
		lp := st.light[:16]
		lp[0], lp[1], lp[2], lp[3] = float32(g.k), float32(g.wd), float32(g.hd), 0
		lp[4], lp[5], lp[6], lp[7] = lf.tm, lf.boost, lf.amp, lf.gain
		lp[8], lp[9], lp[10], lp[11] = lf.vis, lf.cell, lf.rimA/255, float32(corners)
		lp[12], lp[13], lp[14], lp[15] = float32(g.hw), float32(g.hh), 0, 0
		out.Pictures[picEdge] = Picture{W: along, H: 2, Pix: s.edgeTable(), Gen: nextGen()}
		st.reads[0] = append(st.reads[0][:0], picGeometry, picEdge)
		st.passes[0] = GPUPass{Shader: ShaderLight, Target: picLight, Params: lp, Reads: st.reads[0]}
		out.Passes = append(out.Passes, st.passes[0])
		out.Reads = append(out.Reads, picLight)
		p[0], p[1], p[2], p[3] = 1, float32(g.hw+2), float32(g.hh+2), float32(corners)
	}
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

// cardGPU fills in the card for the frame in hand, as draw and paint lay it over the page: its pass,
// drawn when the card has changed, its place, and the thinking dots.
func (s *Screen) cardGPU(g *geometry, now time.Time, p []float32, out *GPUFrame) {
	c, st := &s.card, &s.gpu
	k := g.k
	x0, y0, x1, y1 := c.box(g)
	c.at = image.Rect(int(x0), int(y0), int(math.Ceil(x1)), int(math.Ceil(y1)))
	// What draw would paint the card's picture from: drawn on the cores again in full, whatever it last
	// painted.
	c.look = cardLook{}
	tw, th := cardTexture(g)
	area := c.at.Intersect(image.Rect(0, 0, g.w, g.h))
	area = image.Rectangle{Min: area.Min, Max: area.Min.Add(image.Pt(min(area.Dx(), tw), min(area.Dy(), th)))}
	ccx, ccy := (x0+x1)/2, (y0+y1)/2
	scale := c.scale
	cp := st.cardP[:]
	clear(cp)
	cp[0], cp[1] = float32(area.Min.X), float32(area.Min.Y)
	cp[4], cp[5], cp[6], cp[7] = float32(ccx), float32(ccy), float32((x1-x0)/2), float32((y1-y0)/2)
	cp[8], cp[9], cp[10], cp[11] = float32(cardRadius*k*scale), float32(k*scale), float32(c.alpha), float32(1/scale)
	exact := float32(0)
	if scale == 1 {
		exact = 1
	}
	cp[12], cp[13], cp[14], cp[15] = float32(y0), float32((y1-y0)/2), float32(c.h*k), exact

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
	out.Pictures[picFrost] = Picture{W: c.fw, H: c.fh, Smooth: true, Pix: st.frost, Gen: st.frostPic}
	fa := c.frostAt
	cp[16], cp[17], cp[18], cp[19] = float32(fa.Min.X), float32(fa.Min.Y), float32(4*c.fw), float32(4*c.fh)

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
	st.reads[1] = append(st.reads[1][:0], picFrost)
	for n, l := range layers {
		var other *cardWords
		if len(layers) == 2 {
			other = layers[1-n].cw
		}
		slot := st.wordsSlot(l.cw, other)
		img := l.cw.img
		out.Pictures[picWords0+slot] = Picture{W: img.Rect.Dx(), H: img.Rect.Dy(), Smooth: true, Pix: img.Pix, Gen: l.cw.gen}
		at := 20 + 4*n
		cp[at], cp[at+1], cp[at+2], cp[at+3] = float32(l.alpha), float32(l.dy), float32(img.Rect.Dx()), float32(img.Rect.Dy())
		cp[28+n] = float32(slot)
		st.reads[1] = append(st.reads[1], picWords0+slot)
	}

	// The card's pass, needed again only when what it draws has changed.
	out.Pictures[picCard] = Picture{W: tw, H: th, Gen: 1}
	pics := [3]uint64{st.frostPic, out.Pictures[picWords0].Gen, out.Pictures[picWords1].Gen}
	same := st.cardWas == st.cardP && st.cardWasPics == pics
	st.cardWas, st.cardWasPics = st.cardP, pics
	if !area.Empty() {
		st.passes[1] = GPUPass{Shader: ShaderCard, Target: picCard, Rect: image.Rectangle{Max: area.Size()}, Params: cp, Reads: st.reads[1], Optional: same}
		out.Passes = append(out.Passes, st.passes[1])
		out.Reads = append(out.Reads, picCard)
		p[4], p[5], p[6], p[7] = float32(area.Min.X), float32(area.Min.Y), float32(area.Dx()), float32(area.Dy())
		p[8], p[9] = float32(tw), float32(th)
	}

	// Three dots pulsing one after another at the right of the first line, as draw lays them.
	if c.thinking && c.cur != nil && !c.cur.reply {
		t := float64(now.UnixMilli()%(1<<30)) / 1000
		for i := range 3 {
			ph := 0.5 + 0.5*math.Sin(t*6-float64(i)*0.9)
			lx := (cardW - cardDots + 18 + 11*float64(i)) * k
			ly := 32 * k
			at := 12 + 4*i
			p[at], p[at+1] = float32(ccx+(lx-cardW*k/2)*scale), float32(ccy+(ly-c.h*k/2)*scale)
			p[at+2], p[at+3] = float32((3+ph)*k*scale), float32((0.25+0.6*ph)*c.alpha)
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
