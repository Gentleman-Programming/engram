package chunkcodec

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCompressedEnvelopeRoundTripDoesNotExposePayloadText(t *testing.T) {
	payload := []byte(`{"observations":[{"content":"WAF-SENTINEL-738"}]}`)

	encoded, err := EncodeCompressedEnvelope(payload)
	if err != nil {
		t.Fatalf("EncodeCompressedEnvelope: %v", err)
	}
	if bytes.Contains(encoded, []byte("WAF-SENTINEL-738")) {
		t.Fatal("compressed envelope exposed observation text")
	}

	decoded, err := DecodeCompressedEnvelope(encoded, int64(len(payload)))
	if err != nil {
		t.Fatalf("DecodeCompressedEnvelope: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded payload = %q, want %q", decoded, payload)
	}
}

func TestCompressedEnvelopeRejectsMalformedUnsupportedAndOversizedPayloads(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
		want error
	}{
		{
			name: "malformed compressed data",
			run: func() error {
				_, err := DecodeCompressedEnvelope([]byte("not-gzip"), 32)
				return err
			},
		},
		{
			name: "decoded size limit",
			run: func() error {
				encoded, err := EncodeCompressedEnvelope([]byte(strings.Repeat("a", 33)))
				if err != nil {
					return err
				}
				_, err = DecodeCompressedEnvelope(encoded, 32)
				return err
			},
			want: ErrPayloadTooLarge,
		},
		{
			name: "unsupported version",
			run: func() error {
				_, err := IsCompressedEnvelopeContentType(CompressedEnvelopeMediaType + "; version=2")
				return err
			},
			want: ErrUnsupportedEnvelopeVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatal("expected error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestHasGzipMagic(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{name: "gzip magic with header bytes", payload: []byte{0x1f, 0x8b, 0x08, 0x00}, want: true},
		{name: "exactly gzip magic", payload: []byte{0x1f, 0x8b}, want: true},
		{name: "empty payload", payload: nil, want: false},
		{name: "single gzip byte", payload: []byte{0x1f}, want: false},
		{name: "json payload", payload: []byte(`{"project":"proj-a"}`), want: false},
		{name: "magic bytes not at start", payload: []byte{0x00, 0x1f, 0x8b}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasGzipMagic(tt.payload); got != tt.want {
				t.Fatalf("HasGzipMagic(% x) = %t, want %t", tt.payload, got, tt.want)
			}
		})
	}
}

func TestCompressedEnvelopeAcceptNegotiation(t *testing.T) {
	tests := []struct {
		name   string
		accept string
		want   bool
	}{
		{name: "supported envelope", accept: "application/json, " + CompressedEnvelopeContentType(), want: true},
		{name: "supported envelope with parameters", accept: "application/json; q=0.5, " + CompressedEnvelopeMediaType + "; feature=chunk-pull; version=1", want: true},
		{name: "quality zero", accept: CompressedEnvelopeContentType() + "; q=0", want: false},
		{name: "quality below zero", accept: CompressedEnvelopeContentType() + "; q=-0.1", want: false},
		{name: "quality above one", accept: CompressedEnvelopeContentType() + "; q=1.1", want: false},
		{name: "invalid quality", accept: CompressedEnvelopeContentType() + "; q=invalid", want: false},
		{name: "multiple ranges with acceptable envelope", accept: CompressedEnvelopeContentType() + "; q=0, " + CompressedEnvelopeContentType() + "; q=0.5", want: true},
		{name: "unsupported envelope version", accept: CompressedEnvelopeMediaType + "; version=2", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AcceptsCompressedEnvelope(tt.accept); got != tt.want {
				t.Fatalf("AcceptsCompressedEnvelope(%q) = %t, want %t", tt.accept, got, tt.want)
			}
		})
	}
}
