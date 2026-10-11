package store

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CompatibilityRevision describes this contract, not a release or schema version.
const CompatibilityRevision = 1

type Compatibility struct {
	Revision                          int    `json:"revision"`
	Source                            string `json:"source"`
	Status                            string `json:"status"`
	OfflineAutomaticOpeningAuthorized bool   `json:"offline_automatic_opening_authorized"`
	ActiveReuseAuthorized             bool   `json:"active_reuse_authorized"`
	InstanceID                        string `json:"instance_id,omitempty"`
	RuntimeID                         string `json:"runtime_id,omitempty"`
	Capability                        string `json:"capability,omitempty"`
}

// ProbeCompatibility reads only filesystem metadata, the SQLite header and local
// identity. It deliberately does not use SQLite: even schema 1 runs DDL/repairs
// through New. A header cannot prove shape, so existing databases remain unknown.
// Results are observations, never leases or permission to open a store.
func ProbeCompatibility(dir string) (Compatibility, error) {
	r := Compatibility{Revision: CompatibilityRevision, Source: "offline", Status: "unknown"}
	fail := func() (Compatibility, error) {
		return r, errors.New("compatibility: unsafe or invalid fixture metadata")
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return fail()
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fail()
	}
	// Resolve directory aliases (including macOS /var -> /private/var).
	// Database and identity entries themselves must still be regular files.
	dir = canonical
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return fail()
	}
	before, err := metadataSnapshot(dir)
	if err != nil {
		return fail()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fail()
	}
	hasDB := false
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") || strings.HasSuffix(name, "-journal") || (strings.HasSuffix(name, ".db") && name != "engram.db") {
			return fail()
		}
		if name == "engram.db" {
			hasDB = true
		}
	}
	if !hasDB {
		if len(entries) != 0 {
			return fail()
		}
		r.Status = "fresh"
	} else {
		dbInfo, err := os.Lstat(filepath.Join(dir, "engram.db"))
		if err != nil || !dbInfo.Mode().IsRegular() {
			return fail()
		}
		idInfo, err := os.Lstat(filepath.Join(dir, ".instance-id"))
		if err != nil || !idInfo.Mode().IsRegular() || idInfo.Size() > 128 {
			return fail()
		}
		id, err := os.ReadFile(filepath.Join(dir, ".instance-id"))
		if err != nil || !isValidInstanceID(strings.TrimSpace(string(id))) {
			return fail()
		}
		r.InstanceID = strings.TrimSpace(string(id))
		f, err := os.Open(filepath.Join(dir, "engram.db"))
		if err != nil {
			return fail()
		}
		header := make([]byte, 100)
		_, err = io.ReadFull(f, header)
		opened, statErr := f.Stat()
		_ = f.Close()
		if err != nil || statErr != nil || !os.SameFile(dbInfo, opened) || string(header[:16]) != "SQLite format 3\x00" {
			return fail()
		}
		pageSize := int(binary.BigEndian.Uint16(header[16:18]))
		if pageSize == 1 {
			pageSize = 65536
		}
		if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 || dbInfo.Size()%int64(pageSize) != 0 || header[18] != 1 || header[19] != 1 {
			return fail()
		}
		schema := binary.BigEndian.Uint32(header[60:64])
		if schema > schemaVersion {
			r.Status = "future_schema"
		} else if schema < schemaVersion {
			r.Status = "legacy_schema"
		} else {
			r.Status = "unknown_shape"
		}
	}
	after, err := metadataSnapshot(dir)
	if err != nil || !sameMetadata(before, after) {
		return fail()
	}
	return r, nil
}

func metadataSnapshot(dir string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		result = append(result, info)
	}
	return result, nil
}

func sameMetadata(a, b []os.FileInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name() != b[i].Name() || !os.SameFile(a[i], b[i]) || a[i].Mode() != b[i].Mode() || a[i].Size() != b[i].Size() || !a[i].ModTime().Equal(b[i].ModTime()) {
			return false
		}
	}
	return true
}

// ActiveCompatibility binds successful startup to this actual store handle.
// No SQLite connection/configuration/identity repair is performed by this read.
func (s *Store) ActiveCompatibility() Compatibility {
	r := Compatibility{Revision: CompatibilityRevision, Source: "active", Status: "unknown"}
	if s == nil || s.compatibilityClosed.Load() || !s.compatibilityReady || s.compatibilityRuntimeID == "" || !isValidInstanceID(s.instanceID) || s.generation == nil || s.generation.check() != nil {
		return r
	}
	r.Status, r.ActiveReuseAuthorized = "ready", true
	r.InstanceID, r.RuntimeID = s.instanceID, s.compatibilityRuntimeID
	r.Capability = "startup-verified-store-v1"
	return r
}
