package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

func main() {
	var (
		repoDir = flag.String("repo", ".", "path to repo root (contains build/)")
	)
	flag.Parse()

	buildDir := filepath.Join(*repoDir, "build")
	winDir := filepath.Join(buildDir, "windows")

	if err := os.MkdirAll(winDir, 0o755); err != nil {
		fatal(err)
	}

	app := renderAppIcon(1024)
	if err := writePNG(filepath.Join(buildDir, "appicon.png"), app); err != nil {
		fatal(err)
	}

	tray := renderTrayIcon(512)

	if err := writeICO(filepath.Join(winDir, "icon.ico"), app, []int{256, 128, 64, 48, 32, 16}); err != nil {
		fatal(err)
	}
	if err := writeICO(filepath.Join(winDir, "tray.ico"), tray, []int{64, 32, 24, 16}); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "icongen:", err)
	os.Exit(2)
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func writeICO(path string, src image.Image, sizes []int) error {
	type entry struct {
		size int
		png  []byte
	}
	entries := make([]entry, 0, len(sizes))
	for _, size := range sizes {
		img := scaleDown(src, size, size)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return err
		}
		entries = append(entries, entry{size: size, png: buf.Bytes()})
	}

	// ICONDIR + ICONDIRENTRY[] + image data blobs
	var out bytes.Buffer
	// ICONDIR
	if err := binary.Write(&out, binary.LittleEndian, uint16(0)); err != nil { // reserved
		return err
	}
	if err := binary.Write(&out, binary.LittleEndian, uint16(1)); err != nil { // type (1=icon)
		return err
	}
	if err := binary.Write(&out, binary.LittleEndian, uint16(len(entries))); err != nil {
		return err
	}

	type dirEntry struct {
		width      uint8
		height     uint8
		colorCount uint8
		reserved   uint8
		planes     uint16
		bitCount   uint16
		bytesInRes uint32
		offset     uint32
	}

	dir := make([]dirEntry, 0, len(entries))
	offset := uint32(6 + 16*len(entries))
	for _, e := range entries {
		w := uint8(e.size)
		h := uint8(e.size)
		if e.size >= 256 {
			w, h = 0, 0 // 0 means 256 in ICO
		}
		dir = append(dir, dirEntry{
			width:      w,
			height:     h,
			colorCount: 0,
			reserved:   0,
			planes:     1,
			bitCount:   32,
			bytesInRes: uint32(len(e.png)),
			offset:     offset,
		})
		offset += uint32(len(e.png))
	}

	for _, d := range dir {
		if err := binary.Write(&out, binary.LittleEndian, d); err != nil {
			return err
		}
	}
	for _, e := range entries {
		if _, err := out.Write(e.png); err != nil {
			return err
		}
	}

	return os.WriteFile(path, out.Bytes(), 0o644)
}

func renderAppIcon(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	radius := float64(size) * 0.18
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			a := roundedRectAlpha(float64(x)+0.5, float64(y)+0.5, float64(size), float64(size), radius)
			if a <= 0 {
				continue
			}

			// Background gradient (GitHub-ish blues).
			t := float64(y) / float64(size-1)
			bg := lerpRGBA(
				color.RGBA{R: 0x7d, G: 0xe6, B: 0xff, A: 0xff},
				color.RGBA{R: 0x21, G: 0x8b, B: 0xff, A: 0xff},
				t,
			)

			// Subtle edge shading.
			edge := float64(min4(x, y, size-1-x, size-1-y))
			if edge < float64(size)*0.03 {
				bg = lerpRGBA(bg, color.RGBA{R: 0x0b, G: 0x3d, B: 0x91, A: 0xff}, 0.22*(1-edge/(float64(size)*0.03)))
			}

			set(img, x, y, withAlpha(bg, uint8(float64(bg.A)*a)))
		}
	}

	cx := float64(size) * 0.46
	cy := float64(size) * 0.44
	rOuter := float64(size) * 0.30
	rInner := rOuter - float64(size)*0.055
	rLens := rInner - float64(size)*0.03

	// Outer ring
	fillCircleAA(img, cx, cy, rOuter, color.RGBA{R: 0xd0, G: 0xd7, B: 0xde, A: 0xff})
	fillCircleAA(img, cx, cy, rOuter-float64(size)*0.012, color.RGBA{R: 0x8b, G: 0x94, B: 0x9e, A: 0xff})

	// Inner ring + lens
	fillCircleAA(img, cx, cy, rInner, color.RGBA{R: 0xf6, G: 0xf8, B: 0xfa, A: 0xff})
	fillCircleRadial(img, cx, cy, rLens, color.RGBA{R: 0xb6, G: 0xe3, B: 0xff, A: 0xff}, color.RGBA{R: 0x09, G: 0x69, B: 0xda, A: 0xff})

	// Highlight
	fillCircleAA(img, cx-rLens*0.35, cy-rLens*0.35, rLens*0.18, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x90})

	// Plane (shadow + body)
	plane := []pt{
		{X: cx - float64(size)*0.11, Y: cy - float64(size)*0.03},
		{X: cx + float64(size)*0.15, Y: cy - float64(size)*0.12},
		{X: cx + float64(size)*0.03, Y: cy + float64(size)*0.16},
		{X: cx - float64(size)*0.05, Y: cy + float64(size)*0.03},
	}
	fillPolygon(img, offsetPoly(plane, float64(size)*0.008, float64(size)*0.008), color.RGBA{R: 0, G: 0, B: 0, A: 0x40})
	fillPolygon(img, plane, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})

	// Handle
	handleStartX := cx + rOuter*0.62
	handleStartY := cy + rOuter*0.62
	handleLen := float64(size) * 0.33
	handleW := float64(size) * 0.08
	ang := math.Pi / 4
	dx := math.Cos(ang)
	dy := math.Sin(ang)
	px := -dy
	py := dx
	hx2 := handleStartX + dx*handleLen
	hy2 := handleStartY + dy*handleLen
	handle := []pt{
		{X: handleStartX + px*handleW*0.5, Y: handleStartY + py*handleW*0.5},
		{X: handleStartX - px*handleW*0.5, Y: handleStartY - py*handleW*0.5},
		{X: hx2 - px*handleW*0.5, Y: hy2 - py*handleW*0.5},
		{X: hx2 + px*handleW*0.5, Y: hy2 + py*handleW*0.5},
	}
	fillPolygon(img, handle, color.RGBA{R: 0x30, G: 0x36, B: 0x3d, A: 0xff})
	// Handle cap highlight
	fillPolygon(img, []pt{
		{X: hx2 - px*handleW*0.5, Y: hy2 - py*handleW*0.5},
		{X: hx2 + px*handleW*0.5, Y: hy2 + py*handleW*0.5},
		{X: hx2 + px*handleW*0.3 - dx*handleW*0.4, Y: hy2 + py*handleW*0.3 - dy*handleW*0.4},
		{X: hx2 - px*handleW*0.3 - dx*handleW*0.4, Y: hy2 - py*handleW*0.3 - dy*handleW*0.4},
	}, color.RGBA{R: 0x48, G: 0x51, B: 0x5c, A: 0x90})

	return img
}

func renderTrayIcon(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	cx := float64(size) * 0.46
	cy := float64(size) * 0.46
	rOuter := float64(size) * 0.33
	rInner := rOuter - float64(size)*0.07
	rLens := rInner - float64(size)*0.03

	// Rings
	fillCircleAA(img, cx, cy, rOuter, color.RGBA{R: 0x8b, G: 0x94, B: 0x9e, A: 0xff})
	fillCircleAA(img, cx, cy, rInner-float64(size)*0.012, color.RGBA{R: 0xf6, G: 0xf8, B: 0xfa, A: 0xff})
	fillCircleRadial(img, cx, cy, rLens, color.RGBA{R: 0x9a, G: 0xd6, B: 0xff, A: 0xff}, color.RGBA{R: 0x09, G: 0x69, B: 0xda, A: 0xff})

	plane := []pt{
		{X: cx - float64(size)*0.12, Y: cy - float64(size)*0.03},
		{X: cx + float64(size)*0.16, Y: cy - float64(size)*0.12},
		{X: cx + float64(size)*0.04, Y: cy + float64(size)*0.17},
		{X: cx - float64(size)*0.06, Y: cy + float64(size)*0.04},
	}
	fillPolygon(img, offsetPoly(plane, float64(size)*0.008, float64(size)*0.008), color.RGBA{R: 0, G: 0, B: 0, A: 0x35})
	fillPolygon(img, plane, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})

	// Handle
	handleStartX := cx + rOuter*0.62
	handleStartY := cy + rOuter*0.62
	handleLen := float64(size) * 0.36
	handleW := float64(size) * 0.09
	ang := math.Pi / 4
	dx := math.Cos(ang)
	dy := math.Sin(ang)
	px := -dy
	py := dx
	hx2 := handleStartX + dx*handleLen
	hy2 := handleStartY + dy*handleLen
	handle := []pt{
		{X: handleStartX + px*handleW*0.5, Y: handleStartY + py*handleW*0.5},
		{X: handleStartX - px*handleW*0.5, Y: handleStartY - py*handleW*0.5},
		{X: hx2 - px*handleW*0.5, Y: hy2 - py*handleW*0.5},
		{X: hx2 + px*handleW*0.5, Y: hy2 + py*handleW*0.5},
	}
	fillPolygon(img, handle, color.RGBA{R: 0x30, G: 0x36, B: 0x3d, A: 0xff})

	return img
}

type pt struct {
	X float64
	Y float64
}

func offsetPoly(p []pt, dx, dy float64) []pt {
	out := make([]pt, 0, len(p))
	for _, v := range p {
		out = append(out, pt{X: v.X + dx, Y: v.Y + dy})
	}
	return out
}

func fillPolygon(dst *image.RGBA, poly []pt, c color.RGBA) {
	if len(poly) < 3 {
		return
	}

	minY, maxY := poly[0].Y, poly[0].Y
	for _, p := range poly[1:] {
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	y0 := int(math.Floor(minY))
	y1 := int(math.Ceil(maxY))

	for y := y0; y <= y1; y++ {
		fy := float64(y) + 0.5
		var xs []float64
		for i := 0; i < len(poly); i++ {
			j := (i + 1) % len(poly)
			a := poly[i]
			b := poly[j]
			if (a.Y <= fy && b.Y > fy) || (b.Y <= fy && a.Y > fy) {
				t := (fy - a.Y) / (b.Y - a.Y)
				xs = append(xs, a.X+t*(b.X-a.X))
			}
		}
		if len(xs) < 2 {
			continue
		}
		sortFloats(xs)
		for i := 0; i+1 < len(xs); i += 2 {
			x0 := int(math.Floor(xs[i]))
			x1 := int(math.Ceil(xs[i+1]))
			for x := x0; x <= x1; x++ {
				blend(dst, x, y, c)
			}
		}
	}
}

func sortFloats(a []float64) {
	// Insertion sort is fine for tiny slices.
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}

func fillCircleAA(dst *image.RGBA, cx, cy, r float64, c color.RGBA) {
	minX := int(math.Floor(cx - r - 2))
	maxX := int(math.Ceil(cx + r + 2))
	minY := int(math.Floor(cy - r - 2))
	maxY := int(math.Ceil(cy + r + 2))

	r1 := r - 1.0
	r2 := r + 1.0
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			fx := float64(x) + 0.5
			fy := float64(y) + 0.5
			d := math.Hypot(fx-cx, fy-cy)
			if d <= r1 {
				blend(dst, x, y, c)
				continue
			}
			if d >= r2 {
				continue
			}
			a := clamp01((r2 - d) / (r2 - r1))
			cc := c
			cc.A = uint8(float64(cc.A) * a)
			blend(dst, x, y, cc)
		}
	}
}

func fillCircleRadial(dst *image.RGBA, cx, cy, r float64, inner, outer color.RGBA) {
	minX := int(math.Floor(cx - r - 1))
	maxX := int(math.Ceil(cx + r + 1))
	minY := int(math.Floor(cy - r - 1))
	maxY := int(math.Ceil(cy + r + 1))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			fx := float64(x) + 0.5
			fy := float64(y) + 0.5
			d := math.Hypot(fx-cx, fy-cy)
			if d > r {
				continue
			}
			t := clamp01(d / r)
			c := lerpRGBA(inner, outer, t)
			blend(dst, x, y, c)
		}
	}
}

func roundedRectAlpha(x, y, w, h, r float64) float64 {
	// Distance-based antialiased coverage for a rounded rect at [0..w]x[0..h].
	//
	// Return 0..1 alpha.
	if x < 0 || y < 0 || x > w || y > h {
		return 0
	}
	r = math.Max(0, r)
	r = math.Min(r, math.Min(w, h)/2)

	// Fast inside check for center box.
	if x >= r && x <= w-r && y >= r && y <= h-r {
		return 1
	}

	// Corner distance.
	cx := clamp(x, r, w-r)
	cy := clamp(y, r, h-r)
	d := math.Hypot(x-cx, y-cy)
	if d <= r-1 {
		return 1
	}
	if d >= r+1 {
		return 0
	}
	return clamp01((r + 1 - d) / 2)
}

func scaleDown(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sb := src.Bounds()
	sw := sb.Dx()
	sh := sb.Dy()
	if sw == w && sh == h {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(x, y, src.At(sb.Min.X+x, sb.Min.Y+y))
			}
		}
		return dst
	}

	// Simple area sampling (box filter).
	xScale := float64(sw) / float64(w)
	yScale := float64(sh) / float64(h)
	for y := 0; y < h; y++ {
		y0 := float64(y) * yScale
		y1 := float64(y+1) * yScale
		iy0 := int(math.Floor(y0))
		iy1 := int(math.Ceil(y1))
		for x := 0; x < w; x++ {
			x0 := float64(x) * xScale
			x1 := float64(x+1) * xScale
			ix0 := int(math.Floor(x0))
			ix1 := int(math.Ceil(x1))

			var r, g, b, a float64
			var n float64
			for sy := iy0; sy < iy1; sy++ {
				if sy < 0 || sy >= sh {
					continue
				}
				for sx := ix0; sx < ix1; sx++ {
					if sx < 0 || sx >= sw {
						continue
					}
					cr, cg, cb, ca := src.At(sb.Min.X+sx, sb.Min.Y+sy).RGBA()
					r += float64(cr)
					g += float64(cg)
					b += float64(cb)
					a += float64(ca)
					n++
				}
			}
			if n == 0 {
				continue
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8((r / n) / 257.0),
				G: uint8((g / n) / 257.0),
				B: uint8((b / n) / 257.0),
				A: uint8((a / n) / 257.0),
			})
		}
	}

	return dst
}

func blend(dst *image.RGBA, x, y int, src color.RGBA) {
	if x < 0 || y < 0 || x >= dst.Bounds().Dx() || y >= dst.Bounds().Dy() {
		return
	}
	i := dst.PixOffset(x, y)
	dr := float64(dst.Pix[i+0])
	dg := float64(dst.Pix[i+1])
	db := float64(dst.Pix[i+2])
	da := float64(dst.Pix[i+3]) / 255.0

	sa := float64(src.A) / 255.0
	if sa <= 0 {
		return
	}

	outA := sa + da*(1-sa)
	if outA <= 0 {
		dst.Pix[i+0] = 0
		dst.Pix[i+1] = 0
		dst.Pix[i+2] = 0
		dst.Pix[i+3] = 0
		return
	}
	outR := (float64(src.R)*sa + dr*da*(1-sa)) / outA
	outG := (float64(src.G)*sa + dg*da*(1-sa)) / outA
	outB := (float64(src.B)*sa + db*da*(1-sa)) / outA

	dst.Pix[i+0] = uint8(clamp(outR, 0, 255))
	dst.Pix[i+1] = uint8(clamp(outG, 0, 255))
	dst.Pix[i+2] = uint8(clamp(outB, 0, 255))
	dst.Pix[i+3] = uint8(clamp(outA*255.0, 0, 255))
}

func set(dst *image.RGBA, x, y int, c color.RGBA) {
	if x < 0 || y < 0 || x >= dst.Bounds().Dx() || y >= dst.Bounds().Dy() {
		return
	}
	i := dst.PixOffset(x, y)
	dst.Pix[i+0] = c.R
	dst.Pix[i+1] = c.G
	dst.Pix[i+2] = c.B
	dst.Pix[i+3] = c.A
}

func lerpRGBA(a, b color.RGBA, t float64) color.RGBA {
	t = clamp01(t)
	return color.RGBA{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
		A: uint8(float64(a.A) + (float64(b.A)-float64(a.A))*t),
	}
}

func withAlpha(c color.RGBA, a uint8) color.RGBA {
	c.A = a
	return c
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func min4(a, b, c, d int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	if d < m {
		m = d
	}
	return m
}
