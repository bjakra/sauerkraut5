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
	opFXPass    = 14
	opFXItems   = 15

	// ItemSets is how many sets of items a frame may draw, and MaxItems how many items a set holds.
	ItemSets = 4
	MaxItems = 4096

	// EffectPrograms, EffectTextures and EffectParams are how many programs and textures the helper
	// keeps, by slot, and how many parameters (vec4s) a frame may give its program.
	EffectPrograms = 16
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
	kind uint32 // 0 a frame's; 1 a pass's (defines vec4 pass(vec2 c)); 2 items' (NewItemsProgram)
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

// NewPassProgram makes a pass's program (Pass) for slot from source, which defines vec4 pass(vec2 c), the
// target texture's pixel at c.
func NewPassProgram(slot int, source string) (*Program, error) {
	p, err := NewProgram(slot, source)
	if p != nil {
		p.kind = 1
	}
	return p, err
}

// NewItemsProgram makes items' program (Items) for slot from source: one source for the vertex half,
// under #ifdef VERTEX, which defines void item() and sets gl_Position (at(c) is where the canvas's pixel
// c goes) from the item's a0, a1, a2, its corner and since, and the fragment half, which defines
// vec4 color(), premultiplied. Both have csize, since and p[]; the fragment half page(c) and the
// textures too.
func NewItemsProgram(slot int, source string) (*Program, error) {
	p, err := NewProgram(slot, source)
	if p != nil {
		p.kind = 2
	}
	return p, err
}

// Items are quads drawn over a frame by their own program, each from twelve numbers of its own (a0, a1,
// a2): what moves in a few places over a page, falling drops, a sliding line, where working it out at
// every pixel of the panel would be most of the GPU's time for nothing. Fill Data, three vec4s an item,
// and set Count; the helper copies them with each frame that draws them.
type Items struct {
	Program *Program
	Params  []float32
	Reads   []*Texture
	Count   int
	Data    []float32

	set  int
	buf  *buffer
	sent *helper
}

// NewItems makes a set of up to most items, in set (0 to ItemSets-1): the frames drawing it at once use
// different sets.
func NewItems(set, most int) (*Items, error) {
	if set < 0 || set >= ItemSets || most <= 0 || most > MaxItems {
		return nil, fmt.Errorf("gpu: %d items in set %d", most, set)
	}
	b, err := newBuffer("techo5-items", most*12*4)
	if err != nil {
		return nil, err
	}
	data := unsafe.Slice((*float32)(unsafe.Pointer(&b.mem[0])), most*12)
	return &Items{Data: data, set: set, buf: b}, nil
}

// Close frees the items' memory here.
func (it *Items) Close() { it.buf.close(); it.Data = nil }

// Frame is an effect's frame (PresentFrameOf): its program, numbers and textures; the passes drawn
// before it and the items after it; whether the canvas is the same as the last present's, and whether
// to answer before the panel has it; and, for a frame that moves by itself, how often the helper draws
// it again and for how long.
type Frame struct {
	Program      *Program
	Params       []float32
	Reads        []*Texture
	Passes       []Pass
	Items        []*Items
	Same, Ahead  bool
	Every, Lasts time.Duration
}

// Pass is a program drawn into a texture before a frame's own, for the frame's program to read: what
// need not be worked out at every pixel of the panel, at a size of its own. Rect is the part of the
// target drawn; empty, all of it.
type Pass struct {
	Program *Program
	Target  *Texture
	Params  []float32
	Reads   []*Texture
	Rect    image.Rectangle
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
//
// The passes are drawn first, in order (Pass).
func (d *Display) PresentEffect(src *image.RGBA, p *Program, params []float32, reads []*Texture, same, ahead bool, passes ...Pass) error {
	return d.PresentMoving(src, p, params, reads, same, ahead, 0, 0, passes...)
}

// PresentMoving is PresentEffect for an effect that moves by itself: the helper draws the frame again
// every so often, its passes too, for up to as long as lasts or until it is asked anything, the
// programs' `since` the seconds from the frame's start. echod need not wake for those frames. every 0
// is PresentEffect.
func (d *Display) PresentMoving(src *image.RGBA, p *Program, params []float32, reads []*Texture, same, ahead bool, every, lasts time.Duration, passes ...Pass) error {
	return d.PresentFrameOf(src, &Frame{Program: p, Params: params, Reads: reads, Passes: passes, Same: same, Ahead: ahead, Every: every, Lasts: lasts})
}

// PresentFrameOf shows src (the canvas) as f says.
func (d *Display) PresentFrameOf(src *image.RGBA, f *Frame) error {
	p, params, reads, same, ahead, every, lasts, passes := f.Program, f.Params, f.Reads, f.Same, f.Ahead, f.Every, f.Lasts, f.Passes
	for _, it := range f.Items {
		if len(it.Params) > EffectParams*4 || len(it.Params)%4 != 0 || it.Count < 0 || it.Count*12 > len(it.Data) {
			return fmt.Errorf("gpu: %d items with %d parameters", it.Count, len(it.Params))
		}
	}
	if len(params) > EffectParams*4 || len(params)%4 != 0 {
		return fmt.Errorf("gpu: %d parameters for an effect", len(params))
	}
	for _, ps := range passes {
		if len(ps.Params) > EffectParams*4 || len(ps.Params)%4 != 0 {
			return fmt.Errorf("gpu: %d parameters for a pass", len(ps.Params))
		}
	}
	b, err := d.bufferOf(src)
	if err != nil {
		return err
	}
	if err := d.reopen(b); err != nil {
		return err
	}
	if d.fxParams == nil {
		if d.fxParams, err = newBuffer("techo5-params", EffectParams*4*4); err != nil {
			return err
		}
	}
	for _, ps := range passes {
		if err := sendProgram(ps.Program); err != nil {
			return err
		}
		mask, err := sendTextures(ps.Reads)
		if err != nil {
			return err
		}
		if _, err := sendTextures([]*Texture{ps.Target}); err != nil {
			return err
		}
		var at [2]uint32
		if r := ps.Rect; !r.Empty() {
			at = [2]uint32{uint32(r.Min.X) | uint32(r.Min.Y)<<16, uint32(r.Max.X) | uint32(r.Max.Y)<<16}
		}
		req := request{op: opFXPass, a: [7]uint32{uint32(ps.Program.slot), uint32(ps.Target.slot), uint32(len(ps.Params) / 4), mask, at[0], at[1]}}
		if err := d.sendParams(req, ps.Params); err != nil {
			return err
		}
	}
	for _, it := range f.Items {
		if err := d.sendItems(it); err != nil {
			return err
		}
	}
	if err := sendProgram(p); err != nil {
		return err
	}
	mask, err := sendTextures(reads)
	if err != nil {
		return err
	}
	var flags uint32
	if same && !d.fresh {
		flags |= 1
	}
	if ahead {
		flags |= 2
	}
	var moves [2]uint32
	if every > 0 {
		moves = [2]uint32{uint32(max(every.Milliseconds(), 1)), uint32(lasts.Milliseconds())}
	}
	if err := d.sendParams(request{op: opFXPresent, a: [7]uint32{flags, uint32(p.slot), uint32(len(params) / 4), mask, moves[0], moves[1]}}, params); err != nil {
		return err
	}
	d.fresh = false
	return nil
}

// sendItems gives the running helper a set of items for the frame about to be presented.
func (d *Display) sendItems(it *Items) error {
	if err := sendProgram(it.Program); err != nil {
		return err
	}
	mask, err := sendTextures(it.Reads)
	if err != nil {
		return err
	}
	for i, v := range it.Params {
		*(*uint32)(unsafe.Pointer(&d.fxParams.mem[i*4])) = math.Float32bits(v)
	}
	var fds []int
	var has uint32
	mu.Lock()
	if it.sent == nil || it.sent != running {
		fds, has = append(fds, it.buf.fd), has|1
	}
	params := d.fxParams.sent == nil || d.fxParams.sent != running
	if params {
		fds, has = append(fds, d.fxParams.fd), has|2
	}
	mu.Unlock()
	req := request{op: opFXItems, a: [7]uint32{uint32(it.Program.slot), uint32(it.Count), uint32(len(it.Params) / 4), mask, uint32(it.set), has}}
	_, h, err := callRunning(req, fds, replyWait)
	if errors.Is(err, syscall.ENOSYS) {
		return ErrNoEffects // a helper from before items
	}
	if err != nil {
		return err
	}
	if has&1 != 0 {
		it.sent = h
	}
	if params {
		d.fxParams.sent = h
	}
	return nil
}

// sendProgram has the running helper compile p, if it has not.
func sendProgram(p *Program) error {
	if p.bad != nil && current(p.by) {
		return p.bad
	}
	if current(p.sent) {
		return nil
	}
	r, h, err := callRunning(request{op: opFXProgram, a: [7]uint32{uint32(p.slot), p.kind}}, []int{p.src.fd}, programWait)
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
	return nil
}

// sendTextures gives the running helper the textures as they are now, and says which slots they are.
func sendTextures(ts []*Texture) (mask uint32, err error) {
	for _, t := range ts {
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
				return 0, err
			}
			t.sent = h
		case t.y1 > 0:
			if _, err := doRunning(req, nil, replyWait); err != nil {
				return 0, err
			}
		}
		t.y0, t.y1 = 0, 0
	}
	return mask, nil
}

// sendParams asks req of the running helper with params in the parameters' buffer, which the helper
// takes as it answers.
func (d *Display) sendParams(req request, params []float32) error {
	for i, v := range params {
		*(*uint32)(unsafe.Pointer(&d.fxParams.mem[i*4])) = math.Float32bits(v)
	}
	var fds []int
	mu.Lock()
	if d.fxParams.sent == nil || d.fxParams.sent != running {
		fds = []int{d.fxParams.fd}
	}
	mu.Unlock()
	_, h, err := callRunning(req, fds, replyWait)
	if err != nil {
		if req.op == opFXPass && errors.Is(err, syscall.ENOSYS) {
			return ErrNoEffects // a helper from before passes
		}
		return err
	}
	if fds != nil {
		d.fxParams.sent = h
	}
	return nil
}

// Fresh is whether the helper has no copy of the texture: the next frame that reads it gives it one, or a
// blank one for a pass to draw into.
func (t *Texture) Fresh() bool { return !current(t.sent) }

// Pictures keeps the textures of an effect whose pictures come with generations: one texture to each
// of its slots, made again when a picture's size changes and written again when its generation does.
type Pictures struct {
	First int // the slot of picture 0
	tex   []*Texture
	gen   []uint64
}

// Texture is picture i's texture as the picture is now: w by h, read smoothly when smooth, its pixels
// pix (taken only when gen is not what was taken last; none for a texture a pass draws into).
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
	if gen != ps.gen[i] && pix == nil {
		ps.gen[i] = gen // a pass's target: nothing of it here
	} else if gen != ps.gen[i] {
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
