//go:build linux

package gpu_test

import (
	"errors"
	"image"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/gpu/gputest"
)

func TestMain(m *testing.M) {
	gputest.Main()
	os.Exit(m.Run())
}

// pid is the helper process a log line came from.
func pid(t *testing.T, line string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.Fields(line)[0])
	if err != nil {
		t.Fatalf("log line %q", line)
	}
	return n
}

func noRetryWait(t *testing.T) {
	old := gpu.RetryAfter
	gpu.RetryAfter = 0
	t.Cleanup(func() { gpu.RetryAfter = old })
}

// The helper's own refusal is an answer: it keeps running, and the next request goes to it.
func TestRefusalKeepsTheHelper(t *testing.T) {
	log := gputest.Use(t, gputest.Config{Panel: "4x8", Refuse: syscall.EIO})
	d, _, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for range 2 {
		err := d.Present(nil)
		var st *gpu.StatusError
		if !errors.As(err, &st) || !errors.Is(err, syscall.EIO) {
			t.Fatalf("present: %v, want the helper's EIO", err)
		}
	}
	if n := len(log.Ops("hello")); n != 1 {
		t.Errorf("%d helpers started, want 1", n)
	}
}

// A helper that dies between calls is dropped: the next call reports the transport, not a status; the
// calls after it say there is no helper, and Open starts a new one.
func TestDeadHelperIsDropped(t *testing.T) {
	noRetryWait(t)
	log := gputest.Use(t, gputest.Config{Panel: "4x8", DieAt: 2})
	d, _, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Present(nil); err != nil {
		t.Fatal(err)
	}
	err = d.Present(nil)
	var st *gpu.StatusError
	if err == nil || errors.As(err, &st) {
		t.Fatalf("present on a dead helper: %v, want a transport error", err)
	}
	if err := d.Present(nil); !errors.Is(err, gpu.ErrNoHelper) {
		t.Fatalf("present after the helper was dropped: %v, want ErrNoHelper", err)
	}
	if err := d.Open(); err != nil {
		t.Fatal(err)
	}
	if err := d.Present(nil); err != nil {
		t.Fatalf("present after a restart: %v", err)
	}
	hellos := log.Ops("hello")
	if len(hellos) != 2 || pid(t, hellos[0]) == pid(t, hellos[1]) {
		t.Errorf("helpers: %q, want two", hellos)
	}
	if opens := log.Ops("open"); len(opens) != 2 || !strings.Contains(opens[1], "fd=true") {
		t.Errorf("opens: %q, want the canvas given to the new helper too", opens)
	}
}

// Release stops the helper, which gives the panel back to the kernel; Open takes it again with the
// same canvas.
func TestReleaseStopsTheHelper(t *testing.T) {
	noRetryWait(t)
	log := gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, canvas, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	first := pid(t, log.Ops("hello")[0])
	d.Release(errors.New("test"))
	if !gputest.Gone(first) {
		t.Fatalf("helper %d still running after Release", first)
	}
	canvas.Pix[0] = 77
	if err := d.Open(); err != nil {
		t.Fatal(err)
	}
	if err := d.Present(nil); err != nil {
		t.Fatal(err)
	}
	p := log.Ops("present")
	if len(p) != 1 || !strings.Contains(p[0], "first=77,") || pid(t, p[0]) == first {
		t.Errorf("presents: %q, want the same canvas on a new helper", p)
	}
}

// A picture of the canvas's size other than the canvas goes through the staging buffer: the helper is
// given it as its canvas, and back to the canvas after.
func TestPresentStaging(t *testing.T) {
	log := gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, canvas, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	canvas.Pix[0] = 10
	st, err := d.Staging()
	if err != nil {
		t.Fatal(err)
	}
	st.Pix[0] = 200
	if err := d.Present(st); err != nil {
		t.Fatal(err)
	}
	other := image.NewRGBA(canvas.Rect)
	other.Pix[0] = 150
	if err := d.Present(other); err != nil {
		t.Fatal(err)
	}
	if err := d.Present(canvas); err != nil {
		t.Fatal(err)
	}
	p := log.Ops("present")
	want := []string{"first=200,", "first=150,", "first=10,"}
	for i, w := range want {
		if i >= len(p) || !strings.Contains(p[i], w) {
			t.Fatalf("presents: %q, want %v", p, want)
		}
	}
	if err := d.Present(image.NewRGBA(image.Rect(0, 0, 3, 3))); err == nil {
		t.Error("a picture of another size was taken")
	}
}

// A frame says its byte order, so the GPU draws a frame made for the framebuffer as it is.
func TestFrameLayout(t *testing.T) {
	log := gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, _, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.PresentFrame(make([]byte, 4*8*4), 4, 8, gpu.FrameBGRA, image.Rectangle{}); err != nil {
		t.Fatal(err)
	}
	if f := log.Ops("frame"); len(f) != 1 || !strings.Contains(f[0], "layout=1") {
		t.Errorf("frames: %q, want layout 1", f)
	}
	if err := d.PresentFrame(make([]byte, 10), 4, 8, gpu.FrameBGRA, image.Rectangle{}); err == nil {
		t.Error("a short frame was taken")
	}
}

// A lift the old helper drew with is not given to the next one: the screen puts its lift back to none
// when it falls back, and a gain sent again would draw the panel too bright at the backlight asked.
func TestReleaseForgetsTheLift(t *testing.T) {
	noRetryWait(t)
	log := gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, _, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.SetLift(1.3, false); err != nil {
		t.Fatal(err)
	}
	d.Release(errors.New("test"))
	if err := d.Open(); err != nil {
		t.Fatal(err)
	}
	if l := log.Ops("lift"); len(l) != 1 {
		t.Errorf("lifts %q: the old gain was given to the new helper", l)
	}
}

// Only Open starts a helper: with none running, a lift, a present or a frame says so at once, so a
// caller holding its own lock (the lift's) never waits for a helper to start and compile its shaders.
func TestOnlyOpenStartsTheHelper(t *testing.T) {
	noRetryWait(t)
	log := gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, _, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Release(errors.New("test"))
	if err := d.SetLift(1.2, true); !errors.Is(err, gpu.ErrNoHelper) {
		t.Errorf("lift with no helper: %v", err)
	}
	if err := d.Present(nil); !errors.Is(err, gpu.ErrNoHelper) {
		t.Errorf("present with no helper: %v", err)
	}
	if err := d.PresentFrame(make([]byte, 4*8*4), 4, 8, gpu.FrameRGBA, image.Rectangle{}); !errors.Is(err, gpu.ErrNoHelper) {
		t.Errorf("frame with no helper: %v", err)
	}
	if _, err := d.SetColor(gpu.Color{}); !errors.Is(err, gpu.ErrNoHelper) {
		t.Errorf("color with no helper: %v", err)
	}
	if n := len(log.Ops("hello")); n != 1 {
		t.Fatalf("%d helpers started, want only the first", n)
	}
	if err := d.Open(); err != nil {
		t.Fatal(err)
	}
	if err := d.Present(nil); err != nil {
		t.Fatal(err)
	}
	if n := len(log.Ops("hello")); n != 2 {
		t.Errorf("%d helpers started, want Open's second", n)
	}
}

// The staging picture made while the helper is being given the display: under -race, the two touch
// the staging buffer under the one lock.
func TestStagingWhileOpening(t *testing.T) {
	gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, _, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	done := make(chan error)
	go func() {
		_, err := d.Staging()
		done <- err
	}()
	for range 20 {
		if err := d.Open(); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The screen releasing the display while the lift's ramp is inside SetLift, past the helper's reply:
// the lift goes back to none with the helper, so the gain the ramp asked of the old helper must not
// outlive it, and the next helper is given none.
func TestReleaseDuringALift(t *testing.T) {
	noRetryWait(t)
	log := gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, _, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	gpu.SetAfterLiftCall(func() {
		done := make(chan struct{})
		go func() { // the screen's goroutine
			d.Release(errors.New("a present failed"))
			close(done)
		}()
		<-done
	})
	defer gpu.SetAfterLiftCall(nil)
	_ = d.SetLift(1.3, false)
	gpu.SetAfterLiftCall(nil)
	if err := d.Open(); err != nil {
		t.Fatal(err)
	}
	if l := log.Ops("lift"); len(l) != 1 {
		t.Errorf("lifts %q: the new helper was given the gain asked of the old one", l)
	}
}

// The same, as it happens: the ramp's lifts against the screen's Release and Open on another
// goroutine, for -race.
func TestLiftWhileReleasing(t *testing.T) {
	noRetryWait(t)
	gputest.Use(t, gputest.Config{Panel: "4x8"})
	d, _, err := gpu.OpenDisplay(8, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	stop := make(chan struct{})
	ramp := make(chan struct{})
	go func() {
		defer close(ramp)
		for {
			select {
			case <-stop:
				return
			default:
				_ = d.SetLift(1.3, false)
			}
		}
	}()
	for range 10 {
		d.Release(errors.New("test"))
		_ = d.Open()
	}
	close(stop)
	<-ramp
}

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
	if p := log.Ops("fxprogram"); len(p) != 1 || !strings.Contains(p[0], "slot=2 fd=true") {
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
