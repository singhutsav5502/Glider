// Command geniconpaperplane generates assets/icon.ico — Glider's system
// tray icon, a simple two-tone paper-plane silhouette matching the
// product's name. Pure Go stdlib only (image, image/png): no external
// image libraries or tools (Pillow, ImageMagick, etc.) needed to
// regenerate it. Run with `go run ./tools/geniconpaperplane` from the
// repo root; re-run any time the shape or color needs to change —
// assets/icon.ico is a generated, checked-in artifact, not hand-edited.
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

// mainColor/foldColor: a clean, saturated blue with a darker fold accent
// — reads clearly against both light and dark system tray backgrounds
// (unlike pure white/black, which disappears against one or the other),
// and the two-tone split reads as a folded-paper crease rather than a
// flat, generic arrow/play-button triangle.
var (
	mainColor = color.RGBA{R: 0x4A, G: 0xA3, B: 0xF5, A: 0xFF}
	foldColor = color.RGBA{R: 0x25, G: 0x63, B: 0xAD, A: 0xFF}
)

// point is a normalized (0..1) coordinate, scaled to each target canvas
// size independently so every resolution stays crisp rather than being
// downsampled from one master image.
type point struct{ x, y float64 }

// Simple diagonal dart pointing up-and-right (suggests flight/motion,
// fits "Glider"): nose at upper-right, wingtip at far-left, tail at
// lower-middle. foldPoint is pulled in from the wingtip toward the
// center — its triangle (nose, foldPoint, tail) is entirely inside the
// main triangle by construction, so the fold accent can never poke
// outside the main silhouette regardless of exact placement.
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
	// Two copies from one generation step, so they never drift apart:
	// assets/icon.ico is the canonical/branding copy (README, docs site,
	// etc.); internal/tray/icon.ico is the embeddable copy go:embed reads
	// (must live inside the importing package's own directory tree — an
	// embed pattern can't reach outside it to assets/ at the repo root).
	for _, path := range []string{"assets/icon.ico", "internal/tray/icon.ico"} {
		if err := os.WriteFile(path, icoBytes, 0o644); err != nil {
			fmt.Println("write error:", err)
			os.Exit(1)
		}
	}
	fmt.Println("wrote icon.ico (sizes", sizes, ") to assets/ and internal/tray/")
}

// renderPlane rasterizes mainShape/foldShape onto a size x size
// transparent RGBA canvas via a standard even-odd point-in-polygon scan —
// flat-filled, no anti-aliasing needed for a shape this simple at icon
// sizes. foldShape is tested first and takes priority so its overlap with
// mainShape renders as the darker accent color.
func renderPlane(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	const margin = 0.04
	inset := func(pts []point) []point {
		out := make([]point, len(pts))
		for i, p := range pts {
			out[i] = point{margin + p.x*(1-2*margin), margin + p.y*(1-2*margin)}
		}
		return out
	}
	main_ := inset(mainShape)
	fold := inset(foldShape)

	for py := 0; py < size; py++ {
		fy := (float64(py) + 0.5) / float64(size)
		for px := 0; px < size; px++ {
			fx := (float64(px) + 0.5) / float64(size)
			switch {
			case pointInPolygon(fx, fy, fold):
				img.SetRGBA(px, py, foldColor)
			case pointInPolygon(fx, fy, main_):
				img.SetRGBA(px, py, mainColor)
			}
		}
	}
	return img
}

// pointInPolygon is the standard ray-casting / even-odd crossing test
// (PNPOLY). The edge-intersection interpolation MUST go from pi to pj —
// swapping them (an earlier version of this file did, by accident)
// produces a boundary formula that's wrong everywhere except exactly at
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
