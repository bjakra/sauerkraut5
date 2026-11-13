// Command sk5logo draws the sauerkraut5 mark: TECHO5's, with a faceted S where TECHO5 has its T, the
// same signal arcs on either side and "SAUERKRAUT5" under it, in TECHO5's colors and Google Sans Flex
// (lib/fonts). It writes the boot splash's picture, the README's logo and a square icon:
//
//	go run ./cmd/sk5logo -splash internal/feature/display/assets/sauerkraut5.png \
//		-logo ../logo/sauerkraut5_logo.png -icon ../logo/sauerkraut5_icon.png
//
// The splash's is TECHO5's size (330×276) with the arcs' middle where TECHO5's is (feature/display
// splash.go's arcCenterX, arcCenterY), so the splash's own pulsing arcs leave it as they leave TECHO5's.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// TECHO5's colors, from its mark.
var (
	navy  = color.RGBA{28, 31, 36, 255}
	dark  = color.RGBA{16, 92, 132, 255}
	mid   = color.RGBA{22, 145, 178, 255}
	light = color.RGBA{40, 210, 232, 255}
	five  = color.RGBA{25, 226, 254, 255}
)

// The mark's layout in the splash picture's pixels: the arcs' middle, the S's half width and half
// height around it, the arcs' radii and width and how far they reach above and below the level.
const (
	cx, cy       = 164.0, 105.0
	sHalfW, sTop = 40.0, 8.0
	sBottom      = 202.0
	arcWidth     = 7.0
	arcSpread    = 38 * math.Pi / 180
	wordBaseline = 262.0
	wordWidth    = 312.0
	splashW, spH = 330, 276
)

// The outermost is where the splash's own arcs start out from (arcFrom, at its scale of 1.25).
var arcRadii = []float64{56, 68, 80}

func main() {
	splash := flag.String("splash", "", "the boot splash's picture, 330×276")
	logo := flag.String("logo", "", "the README's logo, at twice the splash's size")
	icon := flag.String("icon", "", "a square icon, the mark alone")
	fontFile := flag.String("font", "internal/lib/fonts/files/googlesansflex/Bold.ttf", "the wordmark's font")
	flag.Parse()
	b, err := os.ReadFile(*fontFile)
	if err != nil {
		fail(err)
	}
	ft, err := opentype.Parse(b)
	if err != nil {
		fail(err)
	}
	if *splash != "" {
		save(*splash, mark(1, ft, true))
	}
	if *logo != "" {
		save(*logo, mark(2, ft, true))
	}
	if *icon != "" {
		save(*icon, square(512))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sk5logo:", err)
	os.Exit(1)
}

func save(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		fail(err)
	}
	if err := png.Encode(f, img); err != nil {
		fail(err)
	}
	if err := f.Close(); err != nil {
		fail(err)
	}
}

// mark is the splash's picture at k times its size, with the wordmark when words.
func mark(k float64, ft *opentype.Font, words bool) *image.RGBA {
	w, h := int(splashW*k), int(spH*k)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(navy), image.Point{}, draw.Src)
	drawArcs(img, k, 0, 0) // behind the S, as TECHO5's are behind its T
	drawS(img, k, 0, 0)
	if words {
		wordmark(img, k, ft)
	}
	return img
}

// square is the mark alone, centered on navy in a square n wide.
func square(n int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	draw.Draw(img, img.Bounds(), image.NewUniform(navy), image.Point{}, draw.Src)
	// The mark spans the outer arcs across and the S down; fitted with a margin.
	span := math.Max(2*(arcRadii[len(arcRadii)-1]+arcWidth), sBottom-sTop)
	k := float64(n) * 0.80 / span
	// Placed so the S's middle is the square's.
	dx := float64(n)/2 - cx*k
	dy := float64(n)/2 - (sTop+sBottom)/2*k
	drawArcs(img, k, dx, dy)
	drawS(img, k, dx, dy)
	return img
}

// fill lays polygon pts (in the splash's pixels, then times k and moved by dx, dy) over img in c.
func fill(img *image.RGBA, c color.RGBA, k, dx, dy float64, pts ...[2]float64) {
	b := img.Bounds()
	r := vector.NewRasterizer(b.Dx(), b.Dy())
	for i, p := range pts {
		x, y := float32(p[0]*k+dx), float32(p[1]*k+dy)
		if i == 0 {
			r.MoveTo(x, y)
		} else {
			r.LineTo(x, y)
		}
	}
	r.ClosePath()
	r.Draw(img, b, image.NewUniform(c), image.Point{})
}

// drawS is the faceted S: an angular S stroked along a path whose corners are cut on the diagonal, so it
// reads as an S and not a 5, with a hook at either end; each stretch of the stroke is cut along its
// length into a darker facet and a lighter one, the way TECHO5's T is cut, with a hair of navy where
// stretches meet.
func drawS(img *image.RGBA, k, dx, dy float64) {
	// The path in a box 100 wide and 200 tall, the stroke 34 wide, then fitted round the arcs' middle.
	const hw = 17.0
	path := [][2]float64{
		{83, 52}, {83, 38}, {62, 17}, // the top hook, and its corner
		{38, 17}, {17, 38}, // the top left corner
		{17, 85}, {32, 100}, // into the middle
		{68, 100}, {83, 115}, // out of it
		{83, 162}, {62, 183}, // the bottom right corner
		{38, 183}, {17, 162}, // the bottom hook's corner
		{17, 148},
	}
	sx := 2 * sHalfW / 100
	sy := (sBottom - sTop) / 200
	at := func(p [2]float64) [2]float64 { return [2]float64{cx - sHalfW + p[0]*sx, sTop + p[1]*sy} }
	// The stroke's two sides at each point of the path: mitered where it turns, square at its ends.
	n := len(path)
	left, right := make([][2]float64, n), make([][2]float64, n)
	normal := func(a, b [2]float64) (float64, float64) {
		ux, uy := b[0]-a[0], b[1]-a[1]
		l := math.Hypot(ux, uy)
		return -uy / l, ux / l
	}
	for i := range path {
		var nx, ny float64
		switch {
		case i == 0:
			nx, ny = normal(path[0], path[1])
		case i == n-1:
			nx, ny = normal(path[n-2], path[n-1])
		default:
			ax, ay := normal(path[i-1], path[i])
			bx, by := normal(path[i], path[i+1])
			m := 1 + ax*bx + ay*by
			nx, ny = (ax+bx)/m, (ay+by)/m
		}
		left[i] = at([2]float64{path[i][0] + nx*hw, path[i][1] + ny*hw})
		right[i] = at([2]float64{path[i][0] - nx*hw, path[i][1] - ny*hw})
	}
	for i := 0; i+1 < n; i++ {
		// Cut along a diagonal: alternately darker and lighter halves, so neighbouring stretches meet
		// light against dark as the T's facets do.
		a, c := dark, mid
		if i%2 == 1 {
			a, c = mid, dark
		}
		fill(img, a, k, dx, dy, left[i], left[i+1], right[i+1])
		fill(img, c, k, dx, dy, left[i], right[i+1], right[i])
	}
	for i := 1; i+1 < n; i++ {
		line(img, navy, k, dx, dy, left[i], right[i], 1.4)
	}
}

// line lays a stroke w wide from a to b.
func line(img *image.RGBA, c color.RGBA, k, dx, dy float64, a, b [2]float64, w float64) {
	ux, uy := b[0]-a[0], b[1]-a[1]
	l := math.Hypot(ux, uy)
	nx, ny := -uy/l*w/2, ux/l*w/2
	fill(img, c, k, dx, dy, [2]float64{a[0] + nx, a[1] + ny}, [2]float64{b[0] + nx, b[1] + ny},
		[2]float64{b[0] - nx, b[1] - ny}, [2]float64{a[0] - nx, a[1] - ny})
}

// drawArcs is the signal on either side, as TECHO5's: arcs round the middle, reaching arcSpread above
// and below the level, their ends rounded.
func drawArcs(img *image.RGBA, k, dx, dy float64) {
	for _, r := range arcRadii {
		for _, side := range []float64{0, math.Pi} {
			const steps = 48
			var pts [][2]float64
			for i := 0; i <= steps; i++ {
				a := side - arcSpread + 2*arcSpread*float64(i)/steps
				pts = append(pts, [2]float64{cx + (r+arcWidth/2)*math.Cos(a), cy + (r+arcWidth/2)*math.Sin(a)})
			}
			for i := steps; i >= 0; i-- {
				a := side - arcSpread + 2*arcSpread*float64(i)/steps
				pts = append(pts, [2]float64{cx + (r-arcWidth/2)*math.Cos(a), cy + (r-arcWidth/2)*math.Sin(a)})
			}
			fill(img, light, k, dx, dy, pts...)
			for _, a := range []float64{side - arcSpread, side + arcSpread} {
				disc(img, light, k, dx, dy, cx+r*math.Cos(a), cy+r*math.Sin(a), arcWidth/2)
			}
		}
	}
}

func disc(img *image.RGBA, c color.RGBA, k, dx, dy, x, y, r float64) {
	var pts [][2]float64
	for i := range 24 {
		a := 2 * math.Pi * float64(i) / 24
		pts = append(pts, [2]float64{x + r*math.Cos(a), y + r*math.Sin(a)})
	}
	fill(img, c, k, dx, dy, pts...)
}

// wordmark is "SAUERKRAUT" in white and the "5" in TECHO5's cyan, as wide as wordWidth, centered under
// the mark.
func wordmark(img *image.RGBA, k float64, ft *opentype.Font) {
	const text, tail = "SAUERKRAUT", "5"
	size := 40.0
	for range 8 {
		face, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: size * k, DPI: 72, Hinting: font.HintingNone})
		if err != nil {
			fail(err)
		}
		w := font.MeasureString(face, text+tail)
		got := float64(w) / 64 / k
		if math.Abs(got-wordWidth) < 0.5 {
			break
		}
		size *= wordWidth / got
	}
	face, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: size * k, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		fail(err)
	}
	d := &font.Drawer{Dst: img, Face: face}
	w := d.MeasureString(text + tail)
	d.Dot = fixed.Point26_6{X: fixed.Int26_6((float64(img.Bounds().Dx())*64 - float64(w)) / 2), Y: fixed.Int26_6(wordBaseline * k * 64)}
	d.Src = image.NewUniform(color.White)
	d.DrawString(text)
	d.Src = image.NewUniform(five)
	d.DrawString(tail)
}
