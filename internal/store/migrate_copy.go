package store

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// MigrateCopyResult is emitted only after migration, integrity verification,
// checked close, and a second unchanged-source observation. It is not a lease.
type MigrateCopyResult struct {
	SourceDir          string `json:"source_dir"`
	OutputDir          string `json:"output_dir"`
	InstanceID         string `json:"instance_id"`
	MigrationCompleted bool   `json:"migration_completed"`
}

type copyEntry struct {
	Info   os.FileInfo
	Digest [32]byte
}

// MigrateCopy never opens the source with SQLite. The operator must stop every
// client and keep both directories under exclusive control throughout copying
// and subsequent promotion. On failure a newly created output is retained, never
// removed; it must not be admitted as a usable store.
func MigrateCopy(source, output string, confirmed bool) (result *MigrateCopyResult, err error) {
	if !confirmed {
		return nil, fmt.Errorf("--confirm-quiescent is required; stop every source client first")
	}
	source, err = copyCanonicalPath(source)
	if err != nil {
		return nil, err
	}
	output, err = copyCanonicalPath(output)
	if err != nil {
		return nil, err
	}
	if copyWithin(source, output) || copyWithin(output, source) {
		return nil, fmt.Errorf("source and output must be disjoint directories")
	}
	if _, e := os.Lstat(output); !os.IsNotExist(e) {
		return nil, fmt.Errorf("output directory must not exist")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source must be a directory")
	}
	// Lexical containment misses filesystem aliases (for example APFS case
	// aliases). Compare the source identity with every existing output ancestor
	// before any output creation; never infer identity by lowercasing paths.
	physicalOverlap, err := copyPhysicalWithin(info, output)
	if err != nil {
		return nil, err
	}
	if physicalOverlap {
		return nil, fmt.Errorf("source and output must be disjoint directories")
	}
	before, err := copyManifest(source)
	if err != nil {
		return nil, err
	}
	if _, ok := before["engram.db"]; !ok {
		return nil, fmt.Errorf("source has no engram.db")
	}
	identity, err := os.ReadFile(filepath.Join(source, ".instance-id"))
	if err != nil {
		return nil, fmt.Errorf("source instance identity: %w", err)
	}
	instance := strings.TrimSpace(string(identity))
	if !isValidInstanceID(instance) {
		return nil, fmt.Errorf("source instance identity is invalid")
	}
	if err = os.Mkdir(output, 0700); err != nil {
		return nil, fmt.Errorf("create new output: %w", err)
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w; incomplete output retained at %s; do not use or promote it", err, output)
		}
	}()
	if err = copyDirectory(source, output, before); err != nil {
		return nil, err
	}
	header, err := os.Open(filepath.Join(output, "engram.db"))
	if err != nil {
		return nil, err
	}
	var magic [16]byte
	_, readErr := io.ReadFull(header, magic[:])
	if err = errors.Join(readErr, header.Close()); err != nil {
		return nil, err
	}
	if string(magic[:]) != "SQLite format 3\x00" {
		return nil, fmt.Errorf("invalid database header")
	}
	// Read-only database access occurs ONLY on the copy. Without immutable=1,
	// SQLite reads the copied WAL and rebuilds SHM in the destination.
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.Join(output, "engram.db")}).String()+"?mode=ro")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	checkErr := copyValidateDB(db)
	closeErr := db.Close()
	if err = errors.Join(checkErr, closeErr); err != nil {
		return nil, err
	}
	after, err := copyManifest(source)
	if err != nil {
		return nil, err
	}
	if !copyManifestsEqual(before, after) {
		return nil, fmt.Errorf("source changed during copy")
	}
	s, err := New(Config{DataDir: output})
	if err != nil {
		return nil, err
	}
	checkErr = copyIntegrity(s.db)
	instanceMatches := s.InstanceID() == instance
	closeErr = s.Close()
	if err = errors.Join(checkErr, closeErr); err != nil {
		return nil, err
	}
	if !instanceMatches {
		return nil, fmt.Errorf("destination instance identity changed")
	}
	after, err = copyManifest(source)
	if err != nil {
		return nil, err
	}
	if !copyManifestsEqual(before, after) {
		return nil, fmt.Errorf("source changed during migration")
	}
	return &MigrateCopyResult{SourceDir: source, OutputDir: output, InstanceID: instance, MigrationCompleted: true}, nil
}

func copyCanonicalPath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("data and output directories must be absolute paths")
	}
	p = filepath.Clean(p)
	for q := p; ; q = filepath.Dir(q) {
		info, e := os.Lstat(q)
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
		if e == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("symlink path component is not allowed: %s", q)
			}
			if q != p && !info.IsDir() {
				return "", fmt.Errorf("non-directory path component: %s", q)
			}
		}
		if filepath.Dir(q) == q {
			break
		}
	}
	return p, nil
}
func copyPhysicalWithin(parent os.FileInfo, child string) (bool, error) {
	for path := child; ; path = filepath.Dir(path) {
		ancestor, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		if err == nil && os.SameFile(parent, ancestor) {
			return true, nil
		}
		if filepath.Dir(path) == path {
			return false, nil
		}
	}
}

func copyWithin(parent, child string) bool {
	r, e := filepath.Rel(parent, child)
	return e == nil && (r == "." || (!strings.HasPrefix(r, ".."+string(filepath.Separator)) && r != ".."))
}

func copyManifest(root string) (map[string]copyEntry, error) {
	out := map[string]copyEntry{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		info, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe source entry: %s", p)
		}
		r, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		if strings.HasSuffix(r, "-journal") {
			return fmt.Errorf("rollback journals are not supported: %s", r)
		}
		entry := copyEntry{Info: info}
		if info.Mode().IsRegular() {
			f, e := os.Open(p)
			if e != nil {
				return e
			}
			opened, e := f.Stat()
			if e != nil || !os.SameFile(info, opened) {
				return errors.Join(fmt.Errorf("source entry changed: %s", r), e, f.Close())
			}
			h := sha256.New()
			_, e = io.Copy(h, f)
			e = errors.Join(e, f.Close())
			if e != nil {
				return e
			}
			copy(entry.Digest[:], h.Sum(nil))
		}
		out[r] = entry
		return nil
	})
	return out, err
}
func copyManifestsEqual(a, b map[string]copyEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for p, x := range a {
		y, ok := b[p]
		if !ok || x.Digest != y.Digest || x.Info.Mode() != y.Info.Mode() || x.Info.Size() != y.Info.Size() || !x.Info.ModTime().Equal(y.Info.ModTime()) || !os.SameFile(x.Info, y.Info) {
			return false
		}
	}
	return true
}
func copyDirectory(source, output string, manifest map[string]copyEntry) error {
	return filepath.WalkDir(source, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		r, e := filepath.Rel(source, p)
		if e != nil {
			return e
		}
		if r == "." {
			return nil
		}
		entry, ok := manifest[r]
		if !ok {
			return fmt.Errorf("source gained entry: %s", r)
		}
		info, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !os.SameFile(entry.Info, info) || info.Mode() != entry.Info.Mode() {
			return fmt.Errorf("source changed: %s", r)
		}
		dst := filepath.Join(output, r)
		if info.IsDir() {
			return os.Mkdir(dst, 0700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe source entry: %s", r)
		}
		if r == "engram.db-shm" {
			return nil
		}
		in, e := os.Open(p)
		if e != nil {
			return e
		}
		opened, e := in.Stat()
		if e != nil || !os.SameFile(info, opened) {
			return errors.Join(fmt.Errorf("source changed: %s", r), e, in.Close())
		}
		out, e := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return errors.Join(e, in.Close())
		}
		h := sha256.New()
		_, e = io.Copy(io.MultiWriter(out, h), in)
		e = errors.Join(e, out.Sync(), out.Close(), in.Close())
		if e != nil {
			return e
		}
		var digest [32]byte
		copy(digest[:], h.Sum(nil))
		if digest != entry.Digest {
			return fmt.Errorf("source bytes changed: %s", r)
		}
		return os.Chmod(dst, info.Mode().Perm())
	})
}

func copyIntegrity(db *sql.DB) error {
	rows, e := db.Query("PRAGMA integrity_check")
	if e != nil {
		return e
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var v string
		if e = rows.Scan(&v); e != nil {
			return e
		}
		if v != "ok" {
			return fmt.Errorf("database integrity check failed")
		}
		count++
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if count != 1 {
		return fmt.Errorf("database integrity check returned no verdict")
	}
	return rows.Close()
}

type copyCatalogObject struct{ Kind, Table, SQL string }

func copyCatalog(db *sql.DB) (map[string]copyCatalogObject, error) {
	rows, e := db.Query(`SELECT name,type,tbl_name,ifnull(sql,'') FROM sqlite_schema`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]copyCatalogObject{}
	for rows.Next() {
		var name string
		var o copyCatalogObject
		if e = rows.Scan(&name, &o.Kind, &o.Table, &o.SQL); e != nil {
			return nil, e
		}
		o.SQL = copySQLWhitespace(o.SQL)
		out[name] = o
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return out, rows.Close()
}

// Supported catalogs are the complete current migration catalog and the 3.3.2
// catalog: local git v3.3.2 -> b81ea5e differs only by session_finalizations and
// the two superseded_by_sync_id columns. Schema version equality alone is never
// admission. Unknown objects, columns, triggers and constraints fail closed.
func copyValidateDB(db *sql.DB) error {
	var v int
	if e := db.QueryRow("PRAGMA user_version").Scan(&v); e != nil {
		return e
	}
	if v != schemaVersion {
		return fmt.Errorf("unsupported database schema version: %d", v)
	}
	if e := copyIntegrity(db); e != nil {
		return e
	}
	actual, e := copyCatalog(db)
	if e != nil {
		return e
	}
	ref, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		return e
	}
	ref.SetMaxOpenConns(1)
	s := &Store{db: ref, hooks: defaultStoreHooks()}
	e = s.migrate()
	if e != nil {
		return errors.Join(e, ref.Close())
	}
	current, e := copyCatalog(ref)
	e = errors.Join(e, ref.Close())
	if e != nil {
		return e
	}
	// Compare top-level table definitions as sets, because SQLite appends ALTER
	// columns while a fresh baseline may declare those columns in another order.
	for name, o := range actual {
		o.SQL = copyNormalizeDDL(o.SQL)
		actual[name] = o
	}
	for name, o := range current {
		o.SQL = copyNormalizeDDL(o.SQL)
		current[name] = o
	}
	if reflect.DeepEqual(actual, current) {
		return nil
	}
	delete(current, "session_finalizations")
	delete(current, "sqlite_autoindex_session_finalizations_1")
	for _, name := range []string{"observations", "sync_delete_tombstones"} {
		o := current[name]
		o.SQL = strings.Replace(o.SQL, "superseded_by_sync_id TEXT,", "", 1)
		current[name] = o
	}
	if reflect.DeepEqual(actual, current) {
		return nil
	}
	return fmt.Errorf("unsupported database catalog; use a core supporting this store's exact shape")
}

// Normalize only whitespace outside quoted SQL; literal default/trigger values
// must remain exact, even when their internal whitespace differs.
func copySQLWhitespace(ddl string) string {
	var out strings.Builder
	quote := byte(0)
	space := false
	for i := 0; i < len(ddl); i++ {
		c := ddl[i]
		if quote != 0 {
			out.WriteByte(c)
			if c == quote {
				if i+1 < len(ddl) && ddl[i+1] == quote {
					i++
					out.WriteByte(ddl[i])
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			space = true
			continue
		}
		if space && out.Len() > 0 {
			out.WriteByte(' ')
		}
		space = false
		out.WriteByte(c)
		if c == '\'' || c == '"' || c == '`' {
			quote = c
		}
	}
	return out.String()
}

func copyNormalizeDDL(ddl string) string {
	if !strings.HasPrefix(ddl, "CREATE TABLE ") {
		return ddl
	}
	start := strings.IndexByte(ddl, '(')
	if start < 0 || !strings.HasSuffix(ddl, ")") {
		return ddl
	}
	body := ddl[start+1 : len(ddl)-1]
	var parts []string
	depth := 0
	quote := byte(0)
	last := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		if quote != 0 {
			if c == quote {
				if i+1 < len(body) && body[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(body[last:i]))
				last = i + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(body[last:]))
	sort.Strings(parts)
	return ddl[:start+1] + strings.Join(parts, ",") + ",)"
}
