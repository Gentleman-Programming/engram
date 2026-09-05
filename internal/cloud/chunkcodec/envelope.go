package chunkcodec

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
)

const (
	// CompressedEnvelopeMediaType identifies the versioned compressed chunk transport.
	// It reduces false-positive inspection of sync payloads; it does not provide secrecy.
	CompressedEnvelopeMediaType = "application/vnd.engram.sync+gzip"
	envelopeVersion             = "1"

	// DefaultMaxDecodedBytes bounds decompression when a peer does not provide a
	// negotiated payload limit. It matches the default cloud push body limit.
	DefaultMaxDecodedBytes int64 = 8 * 1024 * 1024
)

var (
	ErrPayloadTooLarge            = errors.New("compressed payload exceeds decoded size limit")
	ErrUnsupportedEnvelopeVersion = errors.New("unsupported compressed envelope version")
)

// CompressedEnvelopeContentType returns the exact media type for the supported envelope.
func CompressedEnvelopeContentType() string {
	return CompressedEnvelopeMediaType + "; version=" + envelopeVersion
}

// IsCompressedEnvelopeContentType reports whether contentType is the supported envelope.
// A recognized envelope media type with another version is rejected rather than decoded as
// legacy JSON.
func IsCompressedEnvelopeContentType(contentType string) (bool, error) {
	if strings.TrimSpace(contentType) == "" {
		return false, nil
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false, fmt.Errorf("parse content type: %w", err)
	}
	if mediaType != CompressedEnvelopeMediaType {
		return false, nil
	}
	if params["version"] != envelopeVersion {
		return false, ErrUnsupportedEnvelopeVersion
	}
	return true, nil
}

// AcceptsCompressedEnvelope reports whether an Accept header advertises the supported
// envelope. Requests without that explicit advertisement receive legacy JSON.
func AcceptsCompressedEnvelope(accept string) bool {
	for _, value := range strings.Split(accept, ",") {
		ok, err := IsCompressedEnvelopeContentType(strings.TrimSpace(value))
		if err == nil && ok {
			return true
		}
	}
	return false
}

// EncodeCompressedEnvelope gzip-compresses a canonical JSON payload for transport.
func EncodeCompressedEnvelope(payload []byte) ([]byte, error) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		return nil, fmt.Errorf("compress payload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish compressed payload: %w", err)
	}
	return compressed.Bytes(), nil
}

// DecodeCompressedEnvelope decodes a gzip envelope and refuses decoded payloads that
// exceed maxBytes, preventing compressed inputs from bypassing request body limits.
func DecodeCompressedEnvelope(payload []byte, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("decoded size limit must be positive")
	}
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("open compressed payload: %w", err)
	}
	defer reader.Close()

	decoded, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("decode compressed payload: %w", err)
	}
	if int64(len(decoded)) > maxBytes {
		return nil, ErrPayloadTooLarge
	}
	return decoded, nil
}
