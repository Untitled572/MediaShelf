package integrations

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func sampleJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestDecodeRemoteRasterAcceptsOctetStreamByBytes(t *testing.T) {
	jpegBytes := sampleJPEG(t)
	got, mime, err := decodeRemoteRaster(jpegBytes, "images.example", "application/octet-stream")
	if err != nil || mime != "image/jpeg" || !bytes.Equal(got, jpegBytes) {
		t.Fatalf("valid JPEG was not accepted: mime=%q err=%v", mime, err)
	}
}

func TestDecodeRemoteRasterXORLimitedToJavDBUICDN(t *testing.T) {
	plain := sampleJPEG(t)
	encrypted := make([]byte, len(plain))
	for i, b := range plain {
		encrypted[i] = b ^ javDBUIImageXORKey
	}
	got, mime, err := decodeRemoteRaster(encrypted, javDBUIImageHost, "application/octet-stream")
	if err != nil || mime != "image/jpeg" || !bytes.Equal(got, plain) {
		t.Fatalf("known JavDBUI CDN payload did not decode: mime=%q err=%v", mime, err)
	}
	prefixed := append([]byte{javDBUIImageXORKey}, encrypted...)
	got, mime, err = decodeRemoteRaster(prefixed, javDBUIImageHost, "application/octet-stream")
	if err != nil || mime != "image/jpeg" || !bytes.Equal(got, plain) {
		t.Fatalf("prefixed CDN image failed: %s %v", mime, err)
	}
	if _, _, err := decodeRemoteRaster(prefixed, "other.example", "application/octet-stream"); err == nil {
		t.Fatal("decoded prefixed image from unrelated host")
	}
	if _, _, err := decodeRemoteRaster(encrypted, "other.example", "application/octet-stream"); err == nil {
		t.Fatal("accepted XOR-obfuscated bytes from an unrelated host")
	}
}

func TestDecodeRemoteRasterRejectsSVGAndInvalidImage(t *testing.T) {
	for _, body := range [][]byte{[]byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"), []byte("not an image")} {
		if _, _, err := decodeRemoteRaster(body, "images.example", "image/jpeg"); err == nil {
			t.Fatal("accepted non-raster or invalid image bytes")
		}
	}
}
