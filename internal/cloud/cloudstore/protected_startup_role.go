package cloudstore

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed testdata/protected_startup_role.sql
var protectedStartupRoleSQL string

func requireProtectedStartupRole(ctx context.Context, tx *sql.Tx) error {
	var eligible bool
	if err := tx.QueryRowContext(ctx, protectedStartupRoleSQL).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return fmt.Errorf("%w: protected receipt runtime role ineligible", errReceiptAppendUnproven)
	}
	return nil
}
