//go:build linux

package gpu_test

import (
	"errors"
	"image"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu/gputest"
)

// A present ahead says so to the helper, and Sync waits for its frame; a frame that failed after its
// present was answered fails the call after it, and the helper stays.
func TestPresentAhead(t *testing.T) {
	log := gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, canvas, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Present(canvas); err != nil {
		t.Fatal(err)
	}
	if err := d.PresentAhead(canvas); err != nil {
		t.Fatal(err)
	}
	if err := d.Sync(); err != nil {
		t.Fatal(err)
	}
	p := log.Ops("present")
	if len(p) != 2 || strings.HasSuffix(p[0], "ahead") || !strings.HasSuffix(p[1], "ahead") || len(log.Ops("sync")) != 1 {
		t.Fatalf("presents %q, syncs %q: want a plain present, one ahead and a sync", p, log.Ops("sync"))
	}
}

func TestPresentAheadFailsLate(t *testing.T) {
	log := gputest.Use(t, gputest.Config{Panel: "4x8", Late: syscall.EIO})
	d, canvas, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.PresentAhead(canvas); err != nil {
		t.Fatal(err)
	}
	var st *gpu.StatusError
	if err := d.Sync(); !errors.As(err, &st) || st.Errno != syscall.EIO {
		t.Fatalf("sync after a frame that failed: %v, want the helper's EIO", err)
	}
	if err := d.Sync(); err != nil {
		t.Fatalf("the failure was said twice: %v", err)
	}
	if len(log.Ops("hello")) != 1 || !gpu.Available() {
		t.Fatal("the helper did not stay")
	}
}

// An effect's program and textures go to the helper once, a texture's changed rows after that, and
// the canvas is copied again unless the frame says it is the same and the helper has a copy.
func TestPresentEffect(t *testing.T) {
	log := gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, canvas, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	prog, err := gpu.NewProgram(2, "vec3 fx(vec2 c) { return page(c); }")
	if err != nil {
		t.Fatal(err)
	}
	defer prog.Close()
	tex, err := gpu.NewTexture(5, 16, 8, true)
	if err != nil {
		t.Fatal(err)
	}
	defer tex.Close()
	params := []float32{1, 2, 3, 4, 5, 6, 7, 8}
	// The helper has no copy of the canvas yet: "the same" is not believed.
	if err := d.PresentEffect(canvas, prog, params, []*gpu.Texture{tex}, true, false); err != nil {
		t.Fatal(err)
	}
	tex.Touch(2, 5)
	if err := d.PresentEffect(canvas, prog, params, []*gpu.Texture{tex}, true, true); err != nil {
		t.Fatal(err)
	}
	if err := d.PresentEffect(canvas, prog, params[:4], nil, false, false); err != nil {
		t.Fatal(err)
	}
	if p := log.Ops("fxprogram"); len(p) != 1 || !strings.Contains(p[0], "slot=2 pass=0 fd=true") {
		t.Errorf("programs %q: want it given once", p)
	}
	x := log.Ops("fxtexture")
	if len(x) != 2 || !strings.Contains(x[0], "slot=5 16x8 smooth=1 rows=0-0 fd=true") || !strings.Contains(x[1], "rows=2-5 fd=false") {
		t.Errorf("textures %q: want the whole of it once, then the rows touched", x)
	}
	f := log.Ops("fxpresent")
	if len(f) != 3 || !strings.Contains(f[0], "program=2 params=2 reads=32 same=0 fd=true") ||
		!strings.HasSuffix(f[1], "same=1 fd=false ahead") || !strings.Contains(f[2], "params=1 reads=0 same=0 fd=false") {
		t.Errorf("frames %q", f)
	}
}

// A frame's passes are drawn before it, each into its texture, with their own numbers in the one buffer.
func TestPresentEffectPasses(t *testing.T) {
	log := gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, canvas, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	prog, _ := gpu.NewProgram(0, "vec3 fx(vec2 c) { return texture2D(t6, c / csize).rgb; }")
	defer prog.Close()
	light, _ := gpu.NewPassProgram(1, "vec4 pass(vec2 c) { return texture2D(t1, c / 4.0); }")
	defer light.Close()
	src, _ := gpu.NewTexture(1, 4, 2, false)
	defer src.Close()
	to, _ := gpu.NewTexture(6, 4, 2, true)
	defer to.Close()
	pass := gpu.Pass{Program: light, Target: to, Params: []float32{1, 2, 3, 4}, Reads: []*gpu.Texture{src}, Rect: image.Rect(1, 0, 3, 2)}
	for range 2 {
		if err := d.PresentEffect(canvas, prog, nil, []*gpu.Texture{to}, false, true, pass); err != nil {
			t.Fatal(err)
		}
	}
	var fx []string
	for _, l := range log.Lines() {
		if _, op, _ := strings.Cut(l, " "); strings.HasPrefix(op, "fx") {
			fx = append(fx, op)
		}
	}
	want := []string{"fxprogram slot=1 pass=1", "fxtexture slot=1", "fxtexture slot=6", "fxpass program=1 to=6 params=1 reads=2 rect=1-20003 fd=true",
		"fxprogram slot=0 pass=0", "fxpresent program=0 params=0 reads=64",
		"fxpass program=1 to=6 params=1 reads=2 rect=1-20003 fd=false", "fxpresent program=0 params=0 reads=64"}
	if len(fx) != len(want) {
		t.Fatalf("asked %q, want %q", fx, want)
	}
	for i := range want {
		if !strings.HasPrefix(fx[i], want[i]) {
			t.Errorf("asked %q, want %q", fx[i], want[i])
		}
	}
}

// A frame that moves by itself says how often the helper draws it again, and for how long.
func TestPresentMoving(t *testing.T) {
	log := gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, canvas, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	prog, _ := gpu.NewProgram(0, "vec3 fx(vec2 c) { return page(c) * fract(since); }")
	defer prog.Close()
	if err := d.PresentMoving(canvas, prog, nil, nil, false, true, 66*time.Millisecond, 2500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := d.PresentEffect(canvas, prog, nil, nil, true, true); err != nil {
		t.Fatal(err)
	}
	f := log.Ops("fxpresent")
	if len(f) != 2 || !strings.HasSuffix(f[0], "ahead every=66 for=2500") || strings.Contains(f[1], "every") {
		t.Errorf("frames %q", f)
	}
}

// Items go to the helper before the frame that draws them, their buffer and the parameters' once.
func TestPresentItems(t *testing.T) {
	log := gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, canvas, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	prog, _ := gpu.NewProgram(0, "vec3 fx(vec2 c) { return page(c); }")
	defer prog.Close()
	drops, _ := gpu.NewItemsProgram(1, "#ifdef VERTEX\nvoid item() { gl_Position = at(corner); }\n#else\nvec4 color() { return vec4(1.0); }\n#endif\n")
	defer drops.Close()
	it, err := gpu.NewItems(2, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	it.Program, it.Params, it.Count = drops, []float32{1, 2, 3, 4}, 3
	for range 2 {
		if err := d.PresentFrameOf(canvas, &gpu.Frame{Program: prog, Items: []*gpu.Items{it}, Every: 33 * time.Millisecond, Lasts: time.Second}); err != nil {
			t.Fatal(err)
		}
	}
	x := log.Ops("fxitems")
	if len(x) != 2 || !strings.HasSuffix(x[0], "program=1 count=3 params=1 reads=0 set=2 has=3") || !strings.HasSuffix(x[1], "has=0") {
		t.Errorf("items %q", x)
	}
	if p := log.Ops("fxprogram"); len(p) != 2 || !strings.Contains(p[0], "slot=1 pass=2") {
		t.Errorf("programs %q", p)
	}
}

// A helper from before effects says so, and one whose compiler refuses the shader says what it said,
// once: neither is the helper failing.
func TestEffectRefused(t *testing.T) {
	log := gputest.Use(t, gputest.Config{Panel: "4x8", NoEffects: true})
	d, canvas, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	prog, _ := gpu.NewProgram(0, "vec3 fx(vec2 c) { return page(c); }")
	defer prog.Close()
	if err := d.PresentEffect(canvas, prog, nil, nil, false, false); !errors.Is(err, gpu.ErrNoEffects) {
		t.Fatalf("an old helper: %v", err)
	}
	if err := d.Present(canvas); err != nil || len(log.Ops("hello")) != 1 {
		t.Fatalf("the helper did not stay: %v", err)
	}
	d.Close()

	log = gputest.Use(t, gputest.Config{Panel: "4x8", BadProgram: true})
	if d, canvas, err = gpu.OpenDisplay(8, 4, true); err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var refused *gpu.ProgramError
	for range 2 {
		if err := d.PresentEffect(canvas, prog, nil, nil, false, false); !errors.As(err, &refused) || !strings.Contains(refused.Log, "fake") {
			t.Fatalf("a shader the compiler refuses: %v", err)
		}
	}
	if n := len(log.Ops("fxprogram")); n != 1 {
		t.Errorf("the refused program was offered %d times", n)
	}
	if err := d.Present(canvas); err != nil || len(log.Ops("hello")) != 1 {
		t.Fatalf("the helper did not stay: %v", err)
	}
}
