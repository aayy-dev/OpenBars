// mkicons writes Open Bars' Windows icons:
//   - internal/ui/icons/tray-*.ico: signal bars drawn as vectors (crisp at
//     16 px) in green (connected), half green (connecting), orange (problem)
//     and gray (waiting/off);
//   - winres/app.ico and app-*.png: the app icon, the same green bars.
//
// Run from windows/: go run ./tools/mkicons
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

var (
	green  = color.NRGBA{0x2f, 0xc9, 0x5e, 0xff}
	orange = color.NRGBA{0xf2, 0xa3, 0x3a, 0xff}
	gray   = color.NRGBA{0x9a, 0x9a, 0x9a, 0xff}
	dim    = color.NRGBA{0x9a, 0x9a, 0x9a, 0x70}
)

func main() {
	trayDir := filepath.Join("internal", "ui", "icons")
	must(os.MkdirAll(trayDir, 0o755))
	sizes := []int{16, 20, 24, 32, 40, 48, 64}
	for name, spec := range map[string]struct {
		on    int
		color color.NRGBA
	}{
		"connected":  {4, green},
		"connecting": {2, green},
		"problem":    {4, orange},
		"waiting":    {4, gray},
	} {
		var frames []image.Image
		for _, s := range sizes {
			frames = append(frames, bars(s, spec.on, spec.color))
		}
		must(writeICO(filepath.Join(trayDir, "tray-"+name+".ico"), frames))
	}

	// The app icon is the "connected" bars on a transparent background, so
	// the taskbar, Start menu and tray all show the same mark.
	var frames []image.Image
	for _, s := range []int{16, 20, 24, 32, 40, 48, 64, 96, 128, 256} {
		frames = append(frames, bars(s, 4, green))
	}
	must(os.MkdirAll("winres", 0o755))
	must(writeICO(filepath.Join("winres", "app.ico"), frames))
	// go-winres builds the exe's icon from one PNG per size.
	for _, fr := range frames {
		f, err := os.Create(filepath.Join("winres", fmt.Sprintf("app-%d.png", fr.Bounds().Dx())))
		must(err)
		must(png.Encode(f, fr))
		f.Close()
	}
}

// bars draws four rounded bars of rising height, the first `on` in c and
// the rest faint, supersampled 8× for smooth edges.
func bars(size, on int, c color.NRGBA) image.Image {
	const ss = 8
	n := size * ss
	out := image.NewNRGBA(image.Rect(0, 0, n, n))
	unit := float32(n) / 24 // design on a 24-unit grid
	for i := 0; i < 4; i++ {
		h := (5 + float32(i)*4.7) * unit
		w := 3.8 * unit
		x := (1.6 + float32(i)*5.6) * unit
		y := float32(n) - 1.5*unit - h
		mask := roundRect(n, x, y, w, h, w/2)
		col := c
		if i >= on {
			col = dim
		}
		draw.DrawMask(out, out.Bounds(), image.NewUniform(col), image.Point{}, mask, image.Point{}, draw.Over)
	}
	small := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.BiLinear.Scale(small, small.Bounds(), out, out.Bounds(), draw.Src, nil)
	return small
}

func roundRect(n int, x, y, w, h, r float32) *image.Alpha {
	z := vector.NewRasterizer(n, n)
	const k = 0.5523 // cubic approximation of a quarter circle
	z.MoveTo(x+r, y)
	z.LineTo(x+w-r, y)
	z.CubeTo(x+w-r+r*k, y, x+w, y+r-r*k, x+w, y+r)
	z.LineTo(x+w, y+h-r)
	z.CubeTo(x+w, y+h-r+r*k, x+w-r+r*k, y+h, x+w-r, y+h)
	z.LineTo(x+r, y+h)
	z.CubeTo(x+r-r*k, y+h, x, y+h-r+r*k, x, y+h-r)
	z.LineTo(x, y+r)
	z.CubeTo(x, y+r-r*k, x+r-r*k, y, x+r, y)
	z.ClosePath()
	mask := image.NewAlpha(image.Rect(0, 0, n, n))
	z.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	return mask
}

// writeICO stores each frame as a PNG inside an .ico (supported since Vista).
func writeICO(path string, frames []image.Image) error {
	var pngs [][]byte
	for _, f := range frames {
		var b bytes.Buffer
		if err := png.Encode(&b, f); err != nil {
			return err
		}
		pngs = append(pngs, b.Bytes())
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(frames))})
	offset := 6 + 16*len(frames)
	for i, f := range frames {
		s := f.Bounds().Dx()
		dim := byte(s)
		if s >= 256 {
			dim = 0
		}
		out.Write([]byte{dim, dim, 0, 0})
		binary.Write(&out, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(pngs[i])), uint32(offset)})
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		out.Write(p)
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
