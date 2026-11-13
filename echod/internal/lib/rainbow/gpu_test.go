//go:build !sk5_no_rainbow

package rainbow

import (
	"bytes"
	"image"
	"testing"
	"time"
)

// A turn given to a GPU and then drawn on the cores after all (PaintGPU) is the turn Finish draws, frame
// for frame after the entry: FinishGPU moves the turn on as Finish does. Its passes are what the frame needs: the light
// while it is on, the card's whenever it changes, and not when it does not.
func TestFinishGPU(t *testing.T) {
	const w, h = 960, 480
	pg := page(t, w, h)
	faces := Faces{Heard: face(t, 22), Reply: face(t, 26)}
	var cpu, gpu Screen
	var fr GPUFrame
	cpuImg, gpuImg := image.NewRGBA(pg.Rect), image.NewRGBA(pg.Rect)
	start := time.Unix(1_700_000_000, 0)
	// The edge for a GPU is worked out in the background, asked for by the first frame of a turn.
	gpu.Begin(w, h, start, Turn{Phase: "listening"})
	gpu.FinishGPU(gpuImg, faces, &fr)
	for g := gpu.frame.g; ; time.Sleep(10 * time.Millisecond) {
		if g == nil {
			g = gpu.geometryFor(w, h)
		}
		if pix, _ := g.gpuGeometry(); pix != nil {
			break
		}
	}
	gpu = Screen{}
	onGPU, cardPasses, skipped := 0, 0, 0
	for f := range 200 {
		now := start.Add(time.Duration(f) * 50 * time.Millisecond)
		turn := Turn{Phase: "listening", Level: 0.6, Heard: "what is the weather"}
		switch {
		case f >= 150:
			turn = Turn{Phase: "replying", Level: 0.4, Heard: "what is the weather", Reply: "Sunny all day."}
		case f >= 100:
			turn = Turn{Phase: "thinking", Heard: "what is the weather"}
		}
		cpu.Begin(w, h, now, turn)
		copy(cpuImg.Pix, pg.Pix)
		// The card painted afresh, as it is after a frame for the GPU, rather than kept from the frame
		// before when it has moved by less than a pixel (cardLook).
		cpu.card.look = cardLook{}
		atCPU := cpu.Finish(cpuImg, faces)

		gpu.Begin(w, h, now, turn)
		copy(gpuImg.Pix, pg.Pix)
		atGPU, ok := gpu.FinishGPU(gpuImg, faces, &fr)
		if ok {
			if !bytes.Equal(gpuImg.Pix, pg.Pix) {
				t.Fatalf("frame %d: FinishGPU drew on the page", f)
			}
			gpu.PaintGPU(gpuImg)
			onGPU++
			light := false
			for _, ps := range fr.Passes {
				switch ps.Shader {
				case ShaderLight:
					light = true
				case ShaderCard:
					if ps.Optional {
						skipped++
					} else {
						cardPasses++
					}
				}
			}
			if !light {
				t.Errorf("frame %d: no light", f)
			}
		} else {
			atGPU = gpu.Finish(gpuImg, faces)
		}
		if atCPU != atGPU {
			t.Fatalf("frame %d: the card at %v, on the cores at %v", f, atGPU, atCPU)
		}
		// The entry draws from blurred copies of the page made in the background, whichever are ready.
		if ok && !bytes.Equal(cpuImg.Pix, gpuImg.Pix) {
			t.Fatalf("frame %d: not the cores' picture", f)
		}
	}
	// The entry, 1.4 s, is the cores'.
	if onGPU < 160 || onGPU > 175 {
		t.Errorf("%d of 200 frames for the GPU", onGPU)
	}
	if cardPasses == 0 || skipped == 0 {
		t.Errorf("the card's pass drawn %d times and left out %d: want both", cardPasses, skipped)
	}
}
