//go:build linux && !sk5_no_rainbow

// Package ongpu hands lib/rainbow's frames for a GPU to the GPU helper (hardware/gpu): its shaders as
// programs, its pictures as textures, each sent again only when it changes, and its passes, those it
// says are optional only when their texture has lost what they drew.
package ongpu

import (
	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/lib/rainbow"
)

// The helper's slots: the shaders' programs from ProgramSlot, the pictures' textures from TextureSlot.
const (
	ProgramSlot = 0
	TextureSlot = 0
)

// Drawer keeps what the helper is given for the Rainbow.
type Drawer struct {
	progs  [rainbow.GPUShaders]*gpu.Program
	pics   gpu.Pictures
	tex    [rainbow.GPUPictures]*gpu.Texture
	passes []gpu.Pass
	reads  [][]*gpu.Texture
	frame  []*gpu.Texture
	shown  bool // the last frame reached the helper whole: its optional passes are drawn
}

// Frame is fr as the helper takes it: the frame's program, its numbers and textures, and the passes
// drawn before it. Shown says it was presented.
func (d *Drawer) Frame(fr *rainbow.GPUFrame) (*gpu.Program, []float32, []*gpu.Texture, []gpu.Pass, error) {
	shown := d.shown
	d.shown = false
	for i := range d.progs {
		if d.progs[i] != nil {
			continue
		}
		var err error
		if i == rainbow.ShaderFrame {
			d.progs[i], err = gpu.NewProgram(ProgramSlot+i, rainbow.Shaders[i])
		} else {
			d.progs[i], err = gpu.NewPassProgram(ProgramSlot+i, rainbow.Shaders[i])
		}
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}
	d.pics.First = TextureSlot
	for i, p := range fr.Pictures {
		d.tex[i] = nil
		if p.Gen == 0 {
			continue
		}
		t, err := d.pics.Texture(i, p.W, p.H, p.Smooth, p.Pix, p.Gen)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		d.tex[i] = t
	}
	textures := func(into []*gpu.Texture, of []int) []*gpu.Texture {
		into = into[:0]
		for _, i := range of {
			if d.tex[i] != nil {
				into = append(into, d.tex[i])
			}
		}
		return into
	}
	d.passes = d.passes[:0]
	for n, ps := range fr.Passes {
		to := d.tex[ps.Target]
		if to == nil || ps.Optional && shown && !to.Fresh() {
			continue
		}
		for len(d.reads) <= n {
			d.reads = append(d.reads, nil)
		}
		d.reads[n] = textures(d.reads[n], ps.Reads)
		d.passes = append(d.passes, gpu.Pass{Program: d.progs[ps.Shader], Target: to, Params: ps.Params, Reads: d.reads[n], Rect: ps.Rect})
	}
	d.frame = textures(d.frame, fr.Reads)
	return d.progs[rainbow.ShaderFrame], fr.Params[:], d.frame, d.passes, nil
}

// Shown says the frame Frame gave was presented: what its passes drew is in their textures.
func (d *Drawer) Shown() { d.shown = true }

// Close frees what the Drawer keeps here.
func (d *Drawer) Close() {
	for i, p := range d.progs {
		if p != nil {
			p.Close()
			d.progs[i] = nil
		}
	}
	d.pics.Close()
}
