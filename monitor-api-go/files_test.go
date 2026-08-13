package main

import "testing"

// The case this was written for: a real desktop.ini off a Windows machine is
// UTF-16LE, which every NUL-byte check calls a binary.
func TestDecodeTextUTF16(t *testing.T) {
	// "\r\n[.ShellClassInfo]" as Windows stores it.
	body := []byte{0xFF, 0xFE}
	for _, r := range "\r\n[.ShellClassInfo]" {
		body = append(body, byte(r), 0x00)
	}

	got, enc, ok := decodeText(body)
	if !ok {
		t.Fatal("UTF-16LE desktop.ini ditolak sebagai biner")
	}
	if enc != encUTF16LE {
		t.Errorf("encoding = %q, mau %q", enc, encUTF16LE)
	}
	if want := "\r\n[.ShellClassInfo]"; got != want {
		t.Errorf("isi = %q, mau %q", got, want)
	}
}

// Saving has to put the file back the way it was found, or Windows stops
// reading its own config.
func TestEncodeTextRoundTrip(t *testing.T) {
	for _, enc := range []textEncoding{encUTF8, encUTF8BOM, encUTF16LE, encUTF16BE} {
		text := "[.ShellClassInfo]\r\nnama=café — 日本\r\n"
		raw := encodeText(text, enc)
		got, gotEnc, ok := decodeText(raw)
		if !ok {
			t.Errorf("%s: hasil encode sendiri ditolak", enc)
			continue
		}
		if gotEnc != enc {
			t.Errorf("%s: kebaca balik sebagai %s", enc, gotEnc)
		}
		if got != text {
			t.Errorf("%s: isi berubah\n got %q\nwant %q", enc, got, text)
		}
	}
}

func TestDecodeTextRejectsBinary(t *testing.T) {
	cases := map[string][]byte{
		"NUL di tengah":       []byte("halo\x00dunia"),
		"utf-8 rusak":         {0xC3, 0x28, 0x41},
		"BOM tapi ganjil":     {0xFF, 0xFE, 0x41},
		"biner ber-BOM-palsu": {0xFF, 0xFE, 0x00, 0x00, 0x01, 0x00},
	}
	for name, b := range cases {
		if _, _, ok := decodeText(b); ok {
			t.Errorf("%s: diterima sebagai teks", name)
		}
	}
}

func TestImageTypeByExtension(t *testing.T) {
	cases := map[string]string{
		"/a/b/foto.JPG": "image/jpeg",
		"/a/b/x.png":    "image/png",
		"/a/b/x.svg":    "image/svg+xml",
		"/a/b/x.txt":    "",
		"/a/b/noext":    "",
	}
	for p, want := range cases {
		if got := imageType(p); got != want {
			t.Errorf("imageType(%q) = %q, mau %q", p, got, want)
		}
	}
}
