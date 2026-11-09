package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
)

// One bounded ZIP-member authority for staged and final-distribution layouts.
func readArtifactMembers(ctx context.Context, data []byte, expected map[string]bool) (map[string][]byte, error) {
	fail := func(s string) (map[string][]byte, error) { return nil, fmt.Errorf("staging: %s", s) }
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	members := make(map[string][]byte)
	var total uint64
	for _, entry := range z.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !expected[entry.Name] || members[entry.Name] != nil || !entry.Mode().IsRegular() {
			return fail("unexpected, duplicate or nonregular member")
		}
		total += entry.UncompressedSize64
		if entry.UncompressedSize64 > 64<<20 || total > 512<<20 {
			return fail("member size limit")
		}
		r, e := entry.Open()
		if e != nil {
			return nil, e
		}
		b, e := io.ReadAll(io.LimitReader(r, (64<<20)+1))
		closeErr := r.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if uint64(len(b)) != entry.UncompressedSize64 {
			return fail("member size mismatch")
		}
		members[entry.Name] = b
	}
	if len(members) != len(expected) {
		return fail("incomplete inventory")
	}
	return members, nil
}
