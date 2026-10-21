// Command genicon draws the Uley icons (a honeycomb cell with a bee
// stripe) as PNG and ICO files, so the repository needs no binary art tools.
//
//	go run ./tools/genicon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

func hexInside(x, y, cx, cy, r float64) bool {
	// Pointy-top regular hexagon with circumradius r.
	dx, dy := math.Abs(x-cx), math.Abs(y-cy)
	w := r * math.Sqrt(3) / 2
	return dx <= w && dx/math.Sqrt(3)+dy <= r
}

func draw(size int, body, stripe color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4 // supersampling for smooth edges
	s := float64(size)
	cx, cy := s/2, s/2
	outer, inner := s*0.48, s*0.34
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var a, st float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := float64(px) + (float64(sx)+0.5)/ss
					y := float64(py) + (float64(sy)+0.5)/ss
					if !hexInside(x, y, cx, cy, outer) {
						continue
					}
					a++
					// Dark ring between outer and inner cell plus a stripe band.
					if !hexInside(x, y, cx, cy, inner) || math.Abs(y-cy) < s*0.07 {
						st++
					}
				}
			}
			n := float64(ss * ss)
			if a == 0 {
				continue
			}
			t := st / a
			c := color.NRGBA{
				R: uint8(float64(body.R)*(1-t) + float64(stripe.R)*t),
				G: uint8(float64(body.G)*(1-t) + float64(stripe.G)*t),
				B: uint8(float64(body.B)*(1-t) + float64(stripe.B)*t),
				A: uint8(255 * a / n),
			}
			img.SetNRGBA(px, py, c)
		}
	}
	return img
}

func pngBytes(img image.Image) []byte {
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// ico packs PNG images into an .ico container (supported since Vista).
func ico(images [][]byte, sizes []int) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(images))})
	offset := 6 + 16*len(images)
	for i, im := range images {
		dim := uint8(sizes[i])
		if sizes[i] >= 256 {
			dim = 0
		}
		binary.Write(&b, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(im)), uint32(offset)})
		offset += len(im)
	}
	for _, im := range images {
		b.Write(im)
	}
	return b.Bytes()
}

func write(path string, data []byte) {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Fatal(err)
	}
}

func main() {
	honey := color.NRGBA{0xF5, 0xB3, 0x01, 0xFF}
	grey := color.NRGBA{0x9A, 0x93, 0x86, 0xFF}
	dark := color.NRGBA{0x2A, 0x1F, 0x0A, 0xFF}

	for name, body := range map[string]color.NRGBA{"active": honey, "paused": grey} {
		write("client/tray/icons/"+name+".png", pngBytes(draw(64, body, dark)))
		write("client/tray/icons/"+name+".ico", ico([][]byte{pngBytes(draw(32, body, dark))}, []int{32}))
	}
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	var imgs [][]byte
	for _, s := range sizes {
		imgs = append(imgs, pngBytes(draw(s, honey, dark)))
	}
	write("installer/windows/uley.ico", ico(imgs, sizes))
	write("installer/icon-512.png", pngBytes(draw(512, honey, dark)))
}
