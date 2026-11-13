//go:build linux && !dot && !panel

package screen

import (
	"errors"
	"strings"
	"syscall"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu/gputest"
)

// With no lift to keep in step, a frame goes to the GPU ahead: Present returns once the helper has the
// canvas, and Settle waits for the frame to be on the panel.
func TestGPUPresentsAhead(t *testing.T) {
	d, log := gpuDevice(t, gputest.Config{Panel: "4x8"})
	if d.gpu == nil {
		t.Fatal("not drawn by the GPU")
	}
	if err := d.Present(); err != nil {
		t.Fatal(err)
	}
	if p := log.Ops("present"); len(p) != 1 || !strings.HasSuffix(p[0], "ahead") || len(log.Ops("sync")) != 0 {
		t.Fatalf("presents %q, syncs %q: want one present ahead, not waited for", p, log.Ops("sync"))
	}
	if err := d.Settle(); err != nil {
		t.Fatal(err)
	}
	if err := d.Settle(); err != nil { // nothing more to wait for
		t.Fatal(err)
	}
	if n := len(log.Ops("sync")); n != 1 {
		t.Fatalf("%d syncs for one frame", n)
	}
}

// A frame that fails after its present was answered takes the screen back to the cores, whichever
// call finds out: Settle here, which says so, and the next Present, which draws on the framebuffer.
func TestGPULateFailureFallsBack(t *testing.T) {
	d, _ := gpuDevice(t, gputest.Config{Panel: "4x8", Late: syscall.EIO})
	canvas := d.Canvas()
	for i := range canvas.Pix {
		canvas.Pix[i] = 0x80
	}
	if err := d.Present(); err != nil {
		t.Fatal(err)
	}
	if err := d.Settle(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("settle after a frame that failed: %v", err)
	}
	if d.gpu != nil || DrawnByGPU() {
		t.Fatal("still on the GPU")
	}
	if err := d.Present(); err != nil {
		t.Fatal(err)
	}
	if err := d.Settle(); err != nil {
		t.Fatal(err)
	}
	if d.mem[0] != 0x80 {
		t.Fatalf("the framebuffer was not drawn: %x", d.mem[:8])
	}

	// And found by the next Present instead: that frame is the cores'.
	d, _ = gpuDevice(t, gputest.Config{Panel: "4x8", Late: syscall.EIO})
	copy(d.Canvas().Pix, canvas.Pix)
	if err := d.Present(); err != nil {
		t.Fatal(err)
	}
	if err := d.Present(); err != nil {
		t.Fatal(err)
	}
	if err := d.Settle(); err != nil {
		t.Fatal(err)
	}
	if d.gpu != nil || d.mem[0] != 0x80 {
		t.Fatalf("on the GPU %v, framebuffer %x: want the cores to have drawn the frame", d.gpu != nil, d.mem[:8])
	}
}

// An effect is a frame of the GPU's while the GPU draws and nothing else has a hand in the picture.
func TestEffects(t *testing.T) {
	d, log := gpuDevice(t, gputest.Config{Panel: "4x8"})
	prog, err := gpu.NewProgram(1, "vec3 fx(vec2 c) { return page(c); }")
	if err != nil {
		t.Fatal(err)
	}
	defer prog.Close()
	e := &Effect{Program: prog, Params: []float32{1, 0, 0, 0}}
	if !d.CanEffect() {
		t.Fatal("no effects on the GPU")
	}
	if err := d.PresentEffect(e); err != nil {
		t.Fatal(err)
	}
	e.Same = true
	if err := d.PresentEffect(e); err != nil {
		t.Fatal(err)
	}
	if err := d.Settle(); err != nil {
		t.Fatal(err)
	}
	f := log.Ops("fxpresent")
	if len(f) != 2 || !strings.Contains(f[0], "same=0") || !strings.HasSuffix(f[1], "same=1 fd=false ahead") || len(log.Ops("sync")) != 1 {
		t.Fatalf("frames %q, syncs %q", f, log.Ops("sync"))
	}
	// A skin's filter is laid over the canvas on the cores: no effect then.
	d.SetFilter("test", func([]byte, int) {})
	if d.CanEffect() || !errors.Is(d.PresentEffect(e), ErrNoEffect) {
		t.Error("an effect under a filter")
	}
	d.SetFilter("", nil)
	// Nor while the lift is on.
	d.SetLift(true)
	if d.CanEffect() {
		t.Error("an effect with the lift on")
	}
	d.SetLift(false)
	if !d.CanEffect() {
		t.Error("no effects once the filter and the lift are off")
	}
}
