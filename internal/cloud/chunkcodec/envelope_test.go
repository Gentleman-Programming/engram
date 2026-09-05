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

func TestCompressedEnvelopeAcceptNegotiation(t *testing.T) {
	tests := []struct {
		name   string
		accept string
		want   bool
	}{
		{name: "supported envelope", accept: "application/json, " + CompressedEnvelopeContentType(), want: true},
		{name: "supported envelope with parameters", accept: "application/json; q=0.5, " + CompressedEnvelopeMediaType + "; feature=chunk-pull; version=1", want: true},
		{name: "quality zero", accept: CompressedEnvelopeContentType() + "; q=0", want: false},
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
