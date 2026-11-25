// Package cover turns album/playlist art into portrait Yoto card art.
//
// Yoto cards are credit-card shaped (about 5:8). Square album art is placed
// full-width in the middle of the card, and the space above and below is
// filled with a blurred, enlarged, slightly darkened copy of the same art.
// Art that is already roughly portrait is simply scaled and center-cropped.
package cover

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	Width  = 640
	Height = 1024 // 5:8, matching a Yoto card
)

// Portrait decodes an image and renders it as Yoto card art (JPEG).
func Portrait(r io.Reader) ([]byte, error) {
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("decode cover: %w", err)
	}
	dst := image.NewRGBA(image.Rect(0, 0, Width, Height))
	b := src.Bounds()
	aspect := float64(b.Dx()) / float64(b.Dy())

	if aspect < 0.8 {
		// Already portrait (e.g. audiobook covers): fill and crop.
		drawCover(dst, dst.Bounds(), src, draw.CatmullRom)
	} else {
		// Background: fill the card with the art, blurred. Blurring at 1/8
		// scale is cheap; three box passes approximate a Gaussian, and a
		// smooth upscale keeps it free of blocks.
		small := image.NewRGBA(image.Rect(0, 0, Width/8, Height/8))
		drawCover(small, small.Bounds(), src, draw.CatmullRom)
		for range 3 {
			boxBlur(small, 4)
		}
		draw.CatmullRom.Scale(dst, dst.Bounds(), small, small.Bounds(), draw.Src, nil)
		darken(dst, 0.72)

		// Foreground: the art at full width, letterboxed in the middle.
		h := int(float64(Width) / aspect)
		top := (Height - h) / 2
		draw.CatmullRom.Scale(dst, image.Rect(0, top, Width, top+h), src, b, draw.Over, nil)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 88}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// drawCover scales src to fill rect, cropping the overflow (CSS object-fit: cover).
func drawCover(dst draw.Image, rect image.Rectangle, src image.Image, s draw.Scaler) {
	b := src.Bounds()
	want := float64(rect.Dx()) / float64(rect.Dy())
	have := float64(b.Dx()) / float64(b.Dy())
	crop := b
	if have > want { // too wide: trim the sides
		w := int(float64(b.Dy()) * want)
		crop.Min.X = b.Min.X + (b.Dx()-w)/2
		crop.Max.X = crop.Min.X + w
	} else { // too tall: trim top and bottom
		h := int(float64(b.Dx()) / want)
		crop.Min.Y = b.Min.Y + (b.Dy()-h)/2
		crop.Max.Y = crop.Min.Y + h
	}
	s.Scale(dst, rect, src, crop, draw.Src, nil)
}

func darken(img *image.RGBA, f float64) {
	for i := 0; i+3 < len(img.Pix); i += 4 {
		img.Pix[i] = uint8(float64(img.Pix[i]) * f)
		img.Pix[i+1] = uint8(float64(img.Pix[i+1]) * f)
		img.Pix[i+2] = uint8(float64(img.Pix[i+2]) * f)
	}
}

// boxBlur blurs img in place with a (2r+1)-wide box, horizontally then
// vertically, clamping at the edges.
func boxBlur(img *image.RGBA, r int) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	tmp := make([]uint8, len(img.Pix))
	pass := func(from, to []uint8, n, lines int, at func(line, i int) int) {
		for l := 0; l < lines; l++ {
			for i := 0; i < n; i++ {
				var sum [4]int
				for k := -r; k <= r; k++ {
					j := min(max(i+k, 0), n-1)
					o := at(l, j)
					for c := range 4 {
						sum[c] += int(from[o+c])
					}
				}
				o := at(l, i)
				for c := range 4 {
					to[o+c] = uint8(sum[c] / (2*r + 1))
				}
			}
		}
	}
	stride := img.Stride
	pass(img.Pix, tmp, w, h, func(y, x int) int { return y*stride + x*4 })
	pass(tmp, img.Pix, h, w, func(x, y int) int { return y*stride + x*4 })
}
