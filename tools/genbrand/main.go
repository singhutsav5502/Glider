// Command genbrand makes every raster and vector file that shows the Glider
// mark. The mark is a folded paper dart. It leans at the angle of the trailing
// flag that a person types.
//
// This command owns the geometry of the mark and the four colour pairs. No
// other file in the repository draws the dart again. A page that shows the mark
// copies the two paths from docs/site/assets/brand/mark.svg, or it points at
// that file.
//
// Run `go run ./tools/genbrand` from the top directory of the repository. Run it
// again each time that the shape or a colour must change. The files that it
// makes are artifacts, and a person keeps them in the repository. No person
// edits them by hand.
//
// The wordmark lockup and the social card need the Archivo typeface, and this
// command uses only the standard library of Go. Therefore a second command
// makes those two files. Run `python tools/genbrand/wordmark.py` after this
// one. It reads mark.svg for the dart, so the geometry still has one source.
//
// Files that this command writes:
//
//	docs/site/assets/brand/mark.svg      the mark alone, square, no padding
//	docs/site/assets/brand/favicon.svg   the mark at the light pair
//	docs/site/assets/brand/icon-192.png   the mark on ink, 18% padding
//	docs/site/assets/brand/icon-512.png   the mark on ink, 18% padding
//	internal/dashboard/static/favicon.svg  the same favicon, for the dashboard
//	assets/icon.ico                      the tray icon and the window icon
//	internal/tray/icon.ico               the copy that go:embed reads
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// ───────────────────────── the mark ─────────────────────────

// pt is a coordinate on the 100-unit grid of the mark.
type pt struct{ x, y float64 }

// The four corners of the dart. The nose is at the upper right, the wingtip is
// at the far left, and the tail is at the lower middle. The fold is between the
// wingtip and the centre.
//
// The triangle of the wing, which is nose, fold and tail, is fully inside the
// outline of the dart by construction. Therefore the wing can never go outside
// the shape at any size.
var (
	nose    = pt{88, 10}
	wingtip = pt{12, 78}
	fold    = pt{44, 58}
	tail    = pt{58, 90}

	// The larger path is the body. The smaller path is the lit wing.
	bodyPath = []pt{nose, wingtip, fold}
	wingPath = []pt{nose, fold, tail}
)

// The visible extent of the mark inside its 100-unit box. The box has space on
// each side, and a lockup must align on the ink and not on the box.
const (
	visX0, visY0 = 12.0, 10.0
	visX1, visY1 = 88.0, 90.0
)

// visW and visH are the width and the nose-to-tail height of the visible mark.
// The clear space rule of the brand is visH / 4 on each side.
const (
	visW = visX1 - visX0
	visH = visY1 - visY0
)

// ───────────────────────── the colour pairs ─────────────────────────

// Only four pairs exist. A page never recolours the body and the wing
// independently of a pair.
//
// On the citron field the mark goes solid ink. Citron on citron disappears, and
// a third colour on that field breaks it.
type pair struct{ body, wing color.RGBA }

var (
	ink   = color.RGBA{0x12, 0x10, 0x0e, 0xff}
	bone  = color.RGBA{0xf5, 0xf1, 0xe8, 0xff}
	citro = color.RGBA{0xc9, 0xf2, 0x4d, 0xff}
	teal  = color.RGBA{0x0a, 0x7c, 0x8c, 0xff}

	// The two pairs that a generated file uses. The other two exist only on a
	// page: {ink, ink} on a citron field, and {currentColor, currentColor} for
	// one-colour work such as print and a stamp.
	onInk   = pair{bone, citro}
	onLight = pair{ink, teal}
)

func hex(c color.RGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// ───────────────────────── the files ─────────────────────────

const brandDir = "docs/site/assets/brand"

func main() {
	if _, err := os.Stat("go.mod"); err != nil {
		fail("run this from the top directory of the repository: %v", err)
	}
	if err := os.MkdirAll(brandDir, 0o755); err != nil {
		fail("make %s: %v", brandDir, err)
	}

	// The mark alone. The two fills read a CSS variable so that a page which
	// inlines this file can set any of the four pairs. The fallback is the pair
	// for an ink ground, because that is the default field of the site.
	write(filepath.Join(brandDir, "mark.svg"), markSVG(
		`var(--mark-body, `+hex(onInk.body)+`)`,
		`var(--mark-wing, `+hex(onInk.wing)+`)`,
	))

	// The favicon takes the light pair, and the wing is teal. Citron does not
	// survive on the white chrome of a browser tab. A favicon has no cascade
	// above it, so the two fills are fixed and read no variable.
	favicon := markSVG(hex(onLight.body), hex(onLight.wing))
	write(filepath.Join(brandDir, "favicon.svg"), favicon)
	write("internal/dashboard/static/favicon.svg", favicon)

	// The app icons are the mark on an ink field with 18% padding. The field is
	// what makes the mark legible on a home screen or a task bar of any colour,
	// and it is the one place where the mark sits inside a shape.
	for _, size := range []int{192, 512} {
		writePNG(filepath.Join(brandDir, fmt.Sprintf("icon-%d.png", size)),
			render(size, 0.18, ink, onInk))
	}

	// The tray icon takes 10% padding and not 18%. A tray icon is a glyph in a
	// strip and not a tile on a home screen, and at 16 pixels the wider padding
	// leaves too little dart to recognise. 10% keeps the clear space rule,
	// which needs visH / 4, or 5% of the field.
	icoSizes := []int{16, 24, 32, 48, 64, 256}
	var pngs [][]byte
	for _, size := range icoSizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, render(size, 0.10, ink, onInk)); err != nil {
			fail("encode %dpx: %v", size, err)
		}
		pngs = append(pngs, buf.Bytes())
	}
	ico, err := buildICO(icoSizes, pngs)
	if err != nil {
		fail("build ico: %v", err)
	}
	// Two copies in one step, so that the two can never differ. assets/icon.ico
	// is the copy for the brand. internal/tray/icon.ico is the copy that
	// go:embed reads, and an embed pattern cannot reach outside the tree of its
	// own package.
	for _, path := range []string{"assets/icon.ico", "internal/tray/icon.ico"} {
		write(path, string(ico))
	}

	fmt.Println("genbrand: wrote mark.svg, favicon.svg (x2), icon-192.png, icon-512.png, icon.ico (x2)")
	fmt.Println("genbrand: now run `python tools/genbrand/wordmark.py` for logo.svg and og.png")
}

// markSVG returns the mark as a standalone SVG document. Two paths, no strokes,
// no gradients. Every other surface shows exactly these two paths.
func markSVG(body, wing string) string {
	d := func(p []pt) string {
		return fmt.Sprintf("M %g %g L %g %g L %g %g Z",
			p[0].x, p[0].y, p[1].x, p[1].y, p[2].x, p[2].y)
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">` + "\n" +
		`  <path d="` + d(bodyPath) + `" fill="` + body + `"/>` + "\n" +
		`  <path d="` + d(wingPath) + `" fill="` + wing + `"/>` + "\n" +
		`</svg>` + "\n"
}

// ───────────────────────── the raster ─────────────────────────

// samples is the count of samples on each axis inside one pixel. 16 samples per
// pixel give the diagonal edges of the dart a smooth edge at every size. The
// earlier tray icon had no anti-aliasing and drew a hard stair on the long
// edge, which is visible at 32 pixels and above.
const samples = 4

// render draws the mark centred on a square field of the given size. pad is the
// fraction of the field on each side that stays empty.
func render(size int, pad float64, bg color.RGBA, p pair) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// Map the 100-unit grid onto the field. The visible extent of the mark, and
	// not its box, is what gets centred, so that the padding is true padding.
	inner := float64(size) * (1 - 2*pad)
	scale := inner / max(visW, visH)
	offX := (float64(size)-visW*scale)/2 - visX0*scale
	offY := (float64(size)-visH*scale)/2 - visY0*scale

	place := func(src []pt) []pt {
		out := make([]pt, len(src))
		for i, q := range src {
			out[i] = pt{q.x*scale + offX, q.y*scale + offY}
		}
		return out
	}
	bodyPx, wingPx := place(bodyPath), place(wingPath)

	for py := range size {
		for px := range size {
			img.SetRGBA(px, py, bg)

			// The wing is tested first and has priority, because it lies on top
			// of the body.
			cw := coverage(px, py, wingPx)
			cb := coverage(px, py, bodyPx)
			if cw == 0 && cb == 0 {
				continue
			}
			c := over(bg, p.body, cb)
			c = over(c, p.wing, cw)
			img.SetRGBA(px, py, c)
		}
	}
	return img
}

// coverage returns the fraction of pixel (px, py) that the polygon covers, from
// 0 to 1, by a regular grid of samples inside the pixel.
func coverage(px, py int, poly []pt) float64 {
	hit := 0
	for sy := range samples {
		y := float64(py) + (float64(sy)+0.5)/samples
		for sx := range samples {
			x := float64(px) + (float64(sx)+0.5)/samples
			if pointInPolygon(x, y, poly) {
				hit++
			}
		}
	}
	return float64(hit) / float64(samples*samples)
}

// over composites src onto dst at the given alpha. Both are opaque, so a plain
// linear mix is correct and no premultiplication is needed.
func over(dst, src color.RGBA, a float64) color.RGBA {
	mix := func(d, s uint8) uint8 {
		return uint8(float64(d)*(1-a) + float64(s)*a + 0.5)
	}
	return color.RGBA{mix(dst.R, src.R), mix(dst.G, src.G), mix(dst.B, src.B), 0xff}
}

// pointInPolygon is the standard ray-casting crossing test (PNPOLY). The
// interpolation on the edge MUST go from pi to pj. Swapping them gives a
// boundary formula that is wrong everywhere except at pi itself, and it fills
// large regions outside the intended shape.
func pointInPolygon(x, y float64, poly []pt) bool {
	inside := false
	j := len(poly) - 1
	for i := range poly {
		pi, pj := poly[i], poly[j]
		if (pi.y > y) != (pj.y > y) {
			xIntersect := pi.x + (y-pi.y)/(pj.y-pi.y)*(pj.x-pi.x)
			if x < xIntersect {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

// buildICO assembles a valid .ico container in the PNG payload form, which
// Windows supports since Vista. It is an ICONDIR header, then one ICONDIRENTRY
// for each size, each of which points at a raw embedded PNG. The format is
// simple enough to write directly, and it needs no third-party library.
func buildICO(sizes []int, pngs [][]byte) ([]byte, error) {
	if len(sizes) != len(pngs) {
		return nil, fmt.Errorf("sizes and pngs have different lengths")
	}
	var buf bytes.Buffer

	// ICONDIR: reserved(2)=0, type(2)=1 for icon, count(2)=N
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(len(sizes)))

	offset := 6 + 16*len(sizes)
	for i, size := range sizes {
		w, h := size, size
		if w >= 256 {
			w, h = 0, 0 // The format uses 0 to mean 256.
		}
		buf.WriteByte(byte(w))
		buf.WriteByte(byte(h))
		buf.WriteByte(0) // palette: 0 means no palette
		buf.WriteByte(0) // reserved
		binary.Write(&buf, binary.LittleEndian, uint16(1))
		binary.Write(&buf, binary.LittleEndian, uint16(32))
		binary.Write(&buf, binary.LittleEndian, uint32(len(pngs[i])))
		binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		buf.Write(p)
	}
	return buf.Bytes(), nil
}

// ───────────────────────── output ─────────────────────────

func write(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fail("write %s: %v", path, err)
	}
}

func writePNG(path string, img *image.RGBA) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		fail("encode %s: %v", path, err)
	}
	write(path, buf.String())
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "genbrand: "+format+"\n", args...)
	os.Exit(1)
}
