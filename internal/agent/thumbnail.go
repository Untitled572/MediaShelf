package agent

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"os"
)

const maxCoverPixels = 40_000_000

// MakeCoverThumbnail decodes only bounded JPEG/PNG inputs and returns a JPEG
// no larger than 400x600. Source images are never modified.
func MakeCoverThumbnail(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("read cover dimensions: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxCoverPixels {
		return nil, errors.New("cover dimensions are invalid or exceed 40 megapixels")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode cover: %w", err)
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil, errors.New("cover has empty dimensions")
	}
	scale := min(400.0/float64(w), 600.0/float64(h))
	if scale > 1 {
		scale = 1
	}
	dw, dh := max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		sy := bounds.Min.Y + y*h/dh
		for x := 0; x < dw; x++ {
			sx := bounds.Min.X + x*w/dw
			c := color.NRGBAModel.Convert(src.At(sx, sy)).(color.NRGBA)
			a := uint32(c.A)
			r := (uint32(c.R)*a + 255*(255-a) + 127) / 255
			g := (uint32(c.G)*a + 255*(255-a) + 127) / 255
			b := (uint32(c.B)*a + 255*(255-a) + 127) / 255
			dst.SetRGBA(x, y, color.RGBA{uint8(r), uint8(g), uint8(b), 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
