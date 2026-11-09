package store

import (
	"errors"
	"fmt"

	sqlite "modernc.org/sqlite"
)

// sqliteCantopenError keeps driver details available to errors.Is/As without
// rendering potentially private driver text in Store or HTTP error messages.
type sqliteCantopenError struct {
	operation string
	code      int
	cause     error
}

func (e *sqliteCantopenError) Error() string {
	return fmt.Sprintf("SQLite CANTOPEN during %s (code %d)", e.operation, e.code)
}
func (e *sqliteCantopenError) Unwrap() error { return e.cause }

// operation is a hardcoded driver boundary, never SQL, a DSN or user input.
func annotateSQLiteCantopen(err error, operation string) error {
	if err == nil {
		return nil
	}
	var annotated *sqliteCantopenError
	if errors.As(err, &annotated) {
		return err
	}
	var original *sqlite.Error
	if !errors.As(err, &original) || original.Code()&0xff != 14 {
		return err
	}
	return &sqliteCantopenError{operation: operation, code: original.Code(), cause: err}
}
