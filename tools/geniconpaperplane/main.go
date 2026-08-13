// Command geniconpaperplane makes assets/icon.ico. That is the icon of Glider
// in the system tray. It shows a paper plane in two tones, and it agrees with
// the name of the product.
//
// The code uses only the standard library of Go: image and image/png. Therefore
// a person needs no external library and no external tool to make the icon
// again, such as Pillow or ImageMagick.
//
// Run `go run ./tools/geniconpaperplane` from the top directory of the
// repository. Run it again each time that the shape or the colour must change.
// assets/icon.ico is an artifact that this code makes, and a person keeps it in
// the repository. No person edits it by hand.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// mainColor and foldColor: a white body with a fold in grey.
//
// The two tones have high contrast between them. The first pair used blue and a
// darker blue, and those two colours were too near to each other. The current
// pair reads as a fold in paper. It does not read as a plain arrow or a
// triangle for "play".
//
// A thin dark line around the shape, which is outlineColor in renderPlane, stops
// the white body from disappearing against a light background in the tray or in
// the title bar. A pure white shape with no line at its edge does exactly
// that.
var (
	mainColor    = color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	foldColor    = color.RGBA{R: 0x6B, G: 0x72, B: 0x7D, A: 0xFF}
	outlineColor = color.RGBA{R: 0x20, G: 0x22, B: 0x26, A: 0xFF}
)

// point is a coordinate from 0 to 1. The code scales it to each target canvas
// size independently. Therefore each resolution stays sharp, and the code does
// not make a small image from one large image.
type point struct{ x, y float64 }

// A simple dart on a diagonal, which points up and to the right. That direction
// suggests flight and movement, and it agrees with the name "Glider". The nose
// is at the upper right, the end of the wing is at the far left, and the tail is
// at the lower middle.
//
// foldPoint is between the end of the wing and the centre. Its triangle, which
// is the nose, foldPoint and the tail, is fully inside the main triangle by
// construction. Therefore the fold can never go outside the main shape, at each
// exact position.
var (
	nose      = point{0.88, 0.14}
	wingtip   = point{0.12, 0.58}
	tail      = point{0.58, 0.90}
	foldPoint = point{0.42, 0.60}

	mainShape = []point{nose, wingtip, tail}
	foldShape = []point{nose, foldPoint, tail}
)

func main() {
	sizes := []int{16, 24, 32, 48, 64, 256}
	var pngs [][]byte
	for _, size := range sizes {
		img := renderPlane(size)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			fmt.Println("png encode error:", err)
			os.Exit(1)
		}
		pngs = append(pngs, buf.Bytes())
	}

	icoBytes, err := buildICO(sizes, pngs)
	if err != nil {
		fmt.Println("ico build error:", err)
		os.Exit(1)
	}
	// This code makes two copies in one step. Therefore the two copies can never
	// become different.
	//
	// assets/icon.ico is the copy for the brand, and the README and the
	// documentation site use it. internal/tray/icon.ico is the copy that go:embed
	// reads. That copy must be inside the directory tree of the package that
	// imports it, because an embed pattern cannot go outside that tree to assets/ at
	// the top of the repository.
	for _, path := range []string{"assets/icon.ico", "internal/tray/icon.ico"} {
		if err := os.WriteFile(path, icoBytes, 0o644); err != nil {
			fmt.Println("write error:", err)
			os.Exit(1)
		}
	}
	fmt.Println("wrote icon.ico (sizes", sizes, ") to assets/ and internal/tray/")
}

// renderPlane draws mainShape and foldShape on a transparent RGBA canvas of
// size by size. It uses a standard scan that tests if a point is inside a
// polygon, with the even-odd rule.
//
// The fill is flat. A shape this simple, at the size of an icon, needs no
// anti-aliasing.
//
// The code tests foldShape first, and that shape has priority. Therefore the
// area where it covers mainShape gets the darker colour of the fold.
func renderPlane(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	// margin shrunk from the original 0.04 — the shape now fills nearly
	// the whole canvas instead of leaving a wide, mostly-empty border.
	const margin = 0.01
	inset := func(pts []point) []point {
		out := make([]point, len(pts))
		for i, p := range pts {
			out[i] = point{margin + p.x*(1-2*margin), margin + p.y*(1-2*margin)}
		}
		return out
	}
	main_ := inset(mainShape)
	fold := inset(foldShape)

	inside := make([]bool, size*size)
	for py := range size {
		fy := (float64(py) + 0.5) / float64(size)
		for px := range size {
			fx := (float64(px) + 0.5) / float64(size)
			if pointInPolygon(fx, fy, main_) || pointInPolygon(fx, fy, fold) {
				inside[py*size+px] = true
			}
		}
	}

	for py := range size {
		fy := (float64(py) + 0.5) / float64(size)
		for px := range size {
			if !inside[py*size+px] {
				continue
			}
			fx := (float64(px) + 0.5) / float64(size)
			if pointInPolygon(fx, fy, fold) {
				img.SetRGBA(px, py, foldColor)
			} else {
				img.SetRGBA(px, py, mainColor)
			}
		}
	}

	drawOutline(img, inside, size)
	return img
}

// drawOutline dilates the shape's own mask by outlineWidth pixels and
// paints the newly-covered ring outlineColor — a white body with no edge
// definition at all disappears against a light tray/title-bar background,
// which is exactly the failure mode a plain white silhouette would have
// here. Cheap raster dilation, not a true vector stroke, but crisp enough
// at icon sizes and needs no new dependency.
func drawOutline(img *image.RGBA, inside []bool, size int) {
	outlineWidth := max(size/32, 1)
	for py := range size {
		for px := range size {
			if inside[py*size+px] {
				continue
			}
			near := false
			for dy := -outlineWidth; dy <= outlineWidth && !near; dy++ {
				ny := py + dy
				if ny < 0 || ny >= size {
					continue
				}
				for dx := -outlineWidth; dx <= outlineWidth; dx++ {
					nx := px + dx
					if nx < 0 || nx >= size {
						continue
					}
					if inside[ny*size+nx] {
						near = true
						break
					}
				}
			}
			if near {
				img.SetRGBA(px, py, outlineColor)
			}
		}
	}
}

// pointInPolygon is the standard ray-casting / even-odd crossing test
// (PNPOLY). The edge-intersection interpolation MUST go from pi to pj —
// swapping them (an earlier version of this file did, by accident)
// produces a boundary formula that is wrong everywhere except exactly at
// pi itself, silently filling large regions outside the intended shape.
// Caught by rendering a debug scanline and comparing pixel spans against
// hand-computed edge intersections, not by eye — the visual difference
// between "correct triangle" and "bug" was subtle enough at preview scale
// to miss otherwise.
func pointInPolygon(x, y float64, poly []point) bool {
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

// buildICO assembles a minimal, valid .ico container using the modern
// PNG-payload form (supported since Windows Vista) — an ICONDIR header
// plus one ICONDIRENTRY per size, each pointing at a raw embedded PNG,
// rather than the legacy uncompressed BMP/DIB form. No third-party ICO
// library needed; the format is simple enough to hand-encode directly.
func buildICO(sizes []int, pngs [][]byte) ([]byte, error) {
	if len(sizes) != len(pngs) {
		return nil, fmt.Errorf("sizes/pngs length mismatch")
	}
	var buf bytes.Buffer

	// ICONDIR: reserved(2)=0, type(2)=1 (icon), count(2)=N
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(len(sizes)))

	headerSize := 6 + 16*len(sizes)
	offset := headerSize
	for i, size := range sizes {
		w, h := size, size
		if w >= 256 {
			w, h = 0, 0 // ICO convention: 0 means 256
		}
		buf.WriteByte(byte(w))
		buf.WriteByte(byte(h))
		buf.WriteByte(0) // color palette: 0 = no palette (PNG/truecolor)
		buf.WriteByte(0) // reserved
		binary.Write(&buf, binary.LittleEndian, uint16(1))  // color planes
		binary.Write(&buf, binary.LittleEndian, uint16(32)) // bits per pixel
		binary.Write(&buf, binary.LittleEndian, uint32(len(pngs[i])))
		binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		buf.Write(p)
	}
	return buf.Bytes(), nil
}
