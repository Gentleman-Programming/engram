package store

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

//go:embed testdata/migrate_copy_v3.4.0_catalog.json
var v340CatalogJSON []byte

func withV340CatalogDifference(unsupported error, normalized map[string]copyCatalogObject) error {
	var reference map[string]copyCatalogObject
	if err := json.Unmarshal(v340CatalogJSON, &reference); err != nil {
		return unsupported
	}
	var changed, missing []string
	for name, object := range reference {
		actual, exists := normalized[name]
		if !exists {
			missing = append(missing, name)
			continue
		}
		object.SQL = copyNormalizeDDL(copySQLWhitespace(object.SQL))
		if actual != object {
			changed = append(changed, name)
		}
	}
	slices.Sort(changed)
	slices.Sort(missing)
	unknown := len(normalized) - len(reference) + len(missing)
	return fmt.Errorf("%w; v3.4.0 catalog comparison changed=%v missing=%v unknown_objects=%d",
		unsupported, changed, missing, unknown)
}
