//go:build linux

package gpu

import (
	"errors"
	"fmt"
	"image"
	"math"
	"syscall"
	"time"
	"unsafe"
)

// Effects: frames the helper draws through a fragment shader of the caller's, over the canvas, where
// the cores would otherwise draw something over the whole of every frame (tools/gpu/techo5-gpu.c,
// "effects"). The shader's source, the pictures it reads and its numbers are the caller's; the canvas
// then holds only what is under the effect, and is copied to the helper only when it changes.

const (
	opFXProgram = 11
	opFXTexture = 12
	opFXPresent = 13

	// EffectPrograms, EffectTextures and EffectParams are how many programs and textures the helper
	// keeps, by slot, and how many parameters (vec4s) a frame may give its program.
	EffectPrograms = 8
	EffectTextures = 8
	EffectParams   = 32

	// programWait bounds a program's compilation, which on the slowest GPUs takes seconds.
	programWait = 30 * time.Second
)

// ErrNoEffects is a helper from before effects.
var ErrNoEffects = errors.New("gpu: the helper draws no effects")

// ProgramError is a program the helper's shader compiler refused, with what it said. The helper is
// fine; the program is not offered to it again.
type ProgramError struct{ Log string }

func (e *ProgramError) Error() string { return "gpu: the effect's shader was refused: " + e.Log }

// Program is an effect's fragment shader, in a slot of the helper's: source that defines
// vec3 fx(vec2 c), the color of the canvas's pixel at c.
type Program struct {
	slot int
	src  *buffer
	sent *helper // the helper that has compiled it
	bad  error   // what a helper said against it: it is not offered to that helper again
	by   *helper
}

// NewProgram makes a program for slot from source. Nothing is asked of the helper until a frame uses it.
func NewProgram(slot int, source string) (*Program, error) {
	if slot < 0 || slot >= EffectPrograms {
		return nil, fmt.Errorf("gpu: program slot %d", slot)
	}
	b, err := newBuffer("techo5-effect", len(source))
	if err != nil {
		return nil, err
	}
	copy(b.mem, source)
	return &Program{slot: slot, src: b}, nil
}

// Close frees the program's memory here; the helper keeps what it compiled until the slot is used again.
func (p *Program) Close() { p.src.close() }

// Texture is a picture an effect's program reads, in a slot of the helper's (sampler t<slot>): w by h
// pixels of four bytes, R, G, B, A, the first row the top one. Write Pix and say what changed (Touch);
// the helper takes its copy with the next frame that reads it.
type Texture struct {
	Pix []byte

	slot, w, h int
	smooth     bool
	buf        *buffer
	sent       *helper
	y0, y1     int // the rows changed since the helper's copy; y1 0: none
}

// NewTexture makes a texture of w by h for slot, read smoothly between its texels when smooth.
func NewTexture(slot, w, h int, smooth bool) (*Texture, error) {
	if slot < 0 || slot >= EffectTextures || w <= 0 || h <= 0 || w > maxSide || h > maxSide {
		return nil, fmt.Errorf("gpu: a %dx%d texture in slot %d", w, h, slot)
	}
	b, err := newBuffer("techo5-texture", w*h*4)
	if err != nil {
		return nil, err
	}
	return &Texture{Pix: b.mem, slot: slot, w: w, h: h, smooth: smooth, buf: b}, nil
}

// Size is the texture's width and height.
func (t *Texture) Size() (w, h int) { return t.w, t.h }

// Touch says rows y0 up to y1 of Pix have changed.
func (t *Texture) Touch(y0, y1 int) {
	y0, y1 = max(y0, 0), min(y1, t.h)
	if y0 >= y1 {
		return
	}
	if t.y1 == 0 {
		t.y0, t.y1 = y0, y1
		return
	}
	t.y0, t.y1 = min(t.y0, y0), max(t.y1, y1)
}

// Close frees the texture's memory here.
func (t *Texture) Close() { t.buf.close(); t.Pix = nil }

// current is whether h still runs, and the helper running.
func current(h *helper) bool {
	mu.Lock()
	defer mu.Unlock()
	return h != nil && h == running
}

// PresentEffect shows src (the canvas) through p, with params (four to a vec4, p[0] on) and the
// textures it reads. same says the picture is as it was at the last present, of either kind, so the
// helper keeps its copy; ahead is PresentAhead's. A program the helper's compiler refuses is an error
// that names what it said, and is not tried again on that helper.
func (d *Display) PresentEffect(src *image.RGBA, p *Program, params []float32, reads []*Texture, same, ahead bool) error {
	if len(params) > EffectParams*4 || len(params)%4 != 0 {
		return fmt.Errorf("gpu: %d parameters for an effect", len(params))
	}
	b, err := d.bufferOf(src)
	if err != nil {
		return err
	}
	if err := d.reopen(b); err != nil {
		return err
	}
	if p.bad != nil && current(p.by) {
		return p.bad
	}
	if !current(p.sent) {
		r, h, err := callRunning(request{op: opFXProgram, a: [7]uint32{uint32(p.slot)}}, []int{p.src.fd}, programWait)
		switch {
		case errors.Is(err, syscall.ENOSYS):
			return ErrNoEffects
		case errors.Is(err, syscall.EINVAL):
			p.bad, p.by = &ProgramError{Log: cstring(r.wire.text[:])}, h
			return p.bad
		case err != nil:
			return err
		}
		p.sent, p.bad = h, nil
	}
	var mask uint32
	for _, t := range reads {
		var flags uint32
		if t.smooth {
			flags = 1
		}
		req := request{op: opFXTexture, a: [7]uint32{uint32(t.slot), uint32(t.w), uint32(t.h), flags, uint32(t.y0), uint32(t.y1)}}
		mask |= 1 << t.slot
		switch {
		case !current(t.sent):
			req.a[4], req.a[5] = 0, 0
			_, h, err := callRunning(req, []int{t.buf.fd}, replyWait)
			if err != nil {
				return err
			}
			t.sent = h
		case t.y1 > 0:
			if _, err := doRunning(req, nil, replyWait); err != nil {
				return err
			}
		}
		t.y0, t.y1 = 0, 0
	}
	if d.fxParams == nil {
		if d.fxParams, err = newBuffer("techo5-params", EffectParams*4*4); err != nil {
			return err
		}
	}
	for i, v := range params {
		*(*uint32)(unsafe.Pointer(&d.fxParams.mem[i*4])) = math.Float32bits(v)
	}
	var flags uint32
	if same && !d.fresh {
		flags |= 1
	}
	if ahead {
		flags |= 2
	}
	var fds []int
	mu.Lock()
	if d.fxParams.sent == nil || d.fxParams.sent != running {
		fds = []int{d.fxParams.fd}
	}
	mu.Unlock()
	_, h, err := callRunning(request{op: opFXPresent, a: [7]uint32{flags, uint32(p.slot), uint32(len(params) / 4), mask}}, fds, replyWait)
	if err != nil {
		return err
	}
	if fds != nil {
		d.fxParams.sent = h
	}
	d.fresh = false
	return nil
}

// Pictures keeps the textures of an effect whose pictures come with generations: one texture to each
// of its slots, made again when a picture's size changes and written again when its generation does.
type Pictures struct {
	First int // the slot of picture 0
	tex   []*Texture
	gen   []uint64
}

// Texture is picture i's texture as the picture is now: w by h, read smoothly when smooth, its pixels
// pix (taken only when gen is not what was taken last).
func (ps *Pictures) Texture(i, w, h int, smooth bool, pix []byte, gen uint64) (*Texture, error) {
	for len(ps.tex) <= i {
		ps.tex, ps.gen = append(ps.tex, nil), append(ps.gen, 0)
	}
	t := ps.tex[i]
	if t != nil && (t.w != w || t.h != h || t.smooth != smooth) {
		t.Close()
		t, ps.tex[i] = nil, nil
	}
	if t == nil {
		var err error
		if t, err = NewTexture(ps.First+i, w, h, smooth); err != nil {
			return nil, err
		}
		ps.tex[i], ps.gen[i] = t, 0
	}
	if gen != ps.gen[i] {
		if len(pix) < w*h*4 {
			return nil, fmt.Errorf("gpu: %d bytes for a %dx%d picture", len(pix), w, h)
		}
		copy(t.Pix, pix[:w*h*4])
		t.Touch(0, h)
		ps.gen[i] = gen
	}
	return t, nil
}

// Close frees the textures.
func (ps *Pictures) Close() {
	for _, t := range ps.tex {
		if t != nil {
			t.Close()
		}
	}
	ps.tex, ps.gen = nil, nil
}
