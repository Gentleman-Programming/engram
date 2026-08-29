package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRuntimeSQLBoundaryGuard keeps runtime SQLite access behind the generation
// boundary. SQL in a named helper is permitted only when every static callsite
// is directly inside a boundary callback; a Tx suffix is not sufficient.
func TestRuntimeSQLBoundaryGuard(t *testing.T) {
	t.Parallel()

	files := []string{"store.go", "relations.go", "diagnostic.go"}
	parsed := make([]guardFile, 0, len(files))
	for _, name := range files {
		source, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, source, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed = append(parsed, guardFile{name: name, fset: fset, file: file})
	}

	var failures []string
	for _, gf := range parsed {
		failures = append(failures, checkRuntimeSQLFile(gf)...)
	}
	sort.Strings(failures)
	if len(failures) > 0 {
		t.Fatalf("runtime SQL boundary violations:\n%s", strings.Join(failures, "\n"))
	}
}

type guardFile struct {
	name string
	fset *token.FileSet
	file *ast.File
}

var runtimeSQLSelectors = map[string]bool{
	"Query": true, "QueryContext": true, "QueryRow": true, "QueryRowContext": true,
	"Exec": true, "Begin": true, "BeginTx": true, "Commit": true, "Rollback": true,
}

var runtimeHookSelectors = map[string]bool{
	"execHook": true, "queryHook": true, "queryItHook": true, "queryItContextHook": true,
	"beginTxHook": true, "commitHook": true,
}

var runtimeBoundaryNames = map[string]bool{
	"withRead": true, "withTx": true, "withTxUnchecked": true,
}

// runtimeSQLEscapes are narrow, named non-runtime boundaries. Keep this list
// explicit so new database access cannot become an unreviewed exception.
var runtimeSQLEscapes = map[string]bool{
	"DB":                                      true,
	"Close":                                   true,
	"Health":                                  true,
	"execHook":                                true,
	"queryHook":                               true,
	"queryItHook":                             true,
	"queryItContextHook":                      true,
	"beginTxHook":                             true,
	"commitHook":                              true,
	"withTx":                                  true,
	"withTxUnchecked":                         true,
	"primeConnection":                         true,
	"registerPersistWALHook":                  true,
	"readUserVersion":                         true,
	"setUserVersion":                          true,
	"migrate":                                 true,
	"addColumnIfNotExists":                    true,
	"migrateSyncChunksTable":                  true,
	"migrateLegacyObservationsTable":          true,
	"migrateCloudSnapshots":                   true,
	"migrateCloudSnapshotsRedaction":          true,
	"migrateDeferredRelationPayloads":         true,
	"migrateFTS":                              true,
	"rebuildFTS":                              true,
	"ensureFTSTriggers":                       true,
	"createFTSTriggers":                       true,
	"dropFTSTriggers":                         true,
	"migrateFTSContentless":                   true,
	"migrateFTSUnicode":                       true,
	"migrateFTSTriggers":                      true,
	"migrateFTSColumns":                       true,
	"migrateFTSVocabulary":                    true,
	"migrateFTSIndex":                         true,
	"migrateRelationPendingPairIndex":         true,
	"migrateRelationSyncIDs":                  true,
	"migrateRelationAliases":                  true,
	"migrateRelationActorMetadata":            true,
	"migrateRelationEvidence":                 true,
	"migrateRelationSupersession":             true,
	"migrateRelationProjectIndex":             true,
	"migrateObservationProjects":              true,
	"migrateSessionProjects":                  true,
	"migrateSyncMutationProjects":             true,
	"migrateSyncState":                        true,
	"migrateSyncJournal":                      true,
	"migrateSyncDeferred":                     true,
	"migrateSyncLeases":                       true,
	"migrateSyncMutationIdentity":             true,
	"migrateCloudUpgradeState":                true,
	"migrateProjectControls":                  true,
	"migrateProjectSchema":                    true,
	"migrateLegacyProjectSchema":              true,
	"migrateStoreSchema":                      true,
	"migrateSchema":                           true,
	"migrateFTSTable":                         true,
	"migrateFTSTriggersForObservations":       true,
	"migrateFTSTriggersForUserPrompts":        true,
	"migrateFTSTriggersForSessions":           true,
	"migrateFTSObservationTriggers":           true,
	"migrateFTSUserPromptTriggers":            true,
	"migrateFTSSessionTriggers":               true,
	"migrateFTSObservationTable":              true,
	"migrateFTSUserPromptTable":               true,
	"migrateFTSSessionTable":                  true,
	"defaultStoreHooks":                       true,
	"recreateObservationFTSTriggers":          true,
	"recreatePromptFTSTriggers":               true,
	"ftsTableSQL":                             true,
	"ftsTableHasColumn":                       true,
	"repairDeferredRelationPayloadIdentities": true,
	"redactCloudUpgradeSnapshots":             true,
	"migrateProjectSyncIdentityTx":            true,
}

// callbackOnlySQLHelpers contain the few named query helpers whose SQL is safe
// only because this test proves every use is directly inside a boundary callback.
var callbackOnlySQLHelpers = map[string]bool{
	"admitPendingRelationTx":                    true,
	"adoptSessionOwnershipTx":                   true,
	"applyPulledMutationTx":                     true,
	"applyObservationDeleteTx":                  true,
	"applyObservationUpsertTx":                  true,
	"applyPromptDeleteTx":                       true,
	"applyPromptUpsertTx":                       true,
	"applyRelationUpsertTx":                     true,
	"applySessionDeleteTx":                      true,
	"applySessionPayloadTx":                     true,
	"applySyncLifecycleTx":                      true,
	"backfillObservationSyncMutationsTx":        true,
	"backfillPromptSyncMutationsTx":             true,
	"backfillRelationSyncMutationsTx":           true,
	"backfillSessionSyncMutationsTx":            true,
	"backfillProjectSyncMutationsTx":            true,
	"backupSQLiteSQL":                           true,
	"createSessionTx":                           true,
	"deadLetterPulledSessionIdentityTx":         true,
	"enqueueMissingLocalMutationTx":             true,
	"enqueueRescuedProjectMutationsTx":          true,
	"enqueueSyncMutationTx":                     true,
	"evaluateCloudUpgradeLegacyMutationTx":      true,
	"evaluatedRelationTx":                       true,
	"findSessionSummaryCandidatesSQL":           true,
	"foreignRecordOwnerTx":                      true,
	"getObservationBySyncIDTx":                  true,
	"getObservationSQL":                         true,
	"getObservationBySyncIDSQL":                 true,
	"listEnrolledProjectsSQL":                   true,
	"isProjectEnrolledSQL":                      true,
	"getSyncedChunksForTargetSQL":               true,
	"listPendingSyncMutationsSQL":               true,
	"listPendingSyncMutationsAfterSeqSQL":       true,
	"countPendingNonEnrolledSyncMutationsSQL":   true,
	"listProjectNamesSQL":                       true,
	"listProjectsWithStatsSQL":                  true,
	"countObservationsForProjectSQL":            true,
	"recentPromptsSQL":                          true,
	"compactionPromptsSQL":                      true,
	"searchPromptsSQL":                          true,
	"timelineSQL":                               true,
	"searchContextSQL":                          true,
	"searchShortAnyContextSQL":                  true,
	"statsSQL":                                  true,
	"formatCompactionContextSQL":                true,
	"exportRelationMutationsSQL":                true,
	"exportWithProjectScopeSQL":                 true,
	"queryObservationsSQL":                      true,
	"listDeferredProjectsForTargetSQL":          true,
	"countDeferredAndDeadForScopeSQL":           true,
	"listDeferredSQL":                           true,
	"getDeferredSQL":                            true,
	"getObservationTx":                          true,
	"getRelationTx":                             true,
	"getRelationSQL":                            true,
	"getCloudUpgradeStateSQL":                   true,
	"mostRecentActiveSessionSQL":                true,
	"recentSessionsSQL":                         true,
	"allSessionsSQL":                            true,
	"getSyncStateTx":                            true,
	"listPendingProjectMutationsTx":             true,
	"listPendingProjectMutationsTxLike":         true,
	"listDiagnosticSessionsSQL":                 true,
	"listInvalidSessionIdentityEvidenceSQL":     true,
	"listQuarantinedPulledSessionEvidenceSQL":   true,
	"estimateSessionProjectReclassificationSQL": true,
	"loadRescueRecordsTx":                       true,
	"pendingRelationTx":                         true,
	"planRescueRecordsTx":                       true,
	"planRescueSessionsTx":                      true,
	"recordRelationApplyFailureTx":              true,
	"refreshProjectSyncLifecycleTx":             true,
	"refreshProjectSyncStateTx":                 true,
	"refreshSyncLifecycleTx":                    true,
	"relationEvaluatedSQL":                      true,
	"relationExistsSQL":                         true,
	"resolveSessionProjectTx":                   true,
	"resolveWriteProjectTx":                     true,
	"sessionOwnershipTx":                        true,
	"validateCrossProjectGuard":                 true,
	"validRelationProjectTx":                    true,
	"getRelationsForObservationsContextSQL":     true,
	"listRelationsSQL":                          true,
	"countRelationsSQL":                         true,
	"getRelationStatsSQL":                       true,
	"scanProjectSQL":                            true,
}

func checkRuntimeSQLFile(gf guardFile) []string {
	parents := parentMap(gf.file)
	var failures []string
	for _, declaration := range gf.file.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name == nil {
			continue
		}
		enclosing := fn.Name.Name
		if runtimeSQLEscapes[enclosing] {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if node == nil {
				return true
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector := calledSelector(call)
			// Calls within the explicit helper graph are safe by transitive proof.
			// A graph entry from any other function must be lexically guarded.
			if callbackOnlySQLHelpers[selector] && !callbackOnlySQLHelpers[enclosing] && !insideRuntimeBoundary(call, parents) {
				failures = append(failures, guardFailure(gf, enclosing, call.Pos(), selector, "callback-only helper call is outside a runtime boundary callback"))
			}
			if !isRuntimeSQLCall(call, selector) {
				return true
			}
			if callbackOnlySQLHelpers[enclosing] || insideRuntimeBoundary(call, parents) {
				return true
			}
			failures = append(failures, guardFailure(gf, enclosing, call.Pos(), selector, "SQL access is outside a runtime boundary callback"))
			return true
		})
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || !isStoreDBSelector(selector) || callbackOnlySQLHelpers[enclosing] || insideRuntimeBoundary(selector, parents) {
				return true
			}
			failures = append(failures, guardFailure(gf, enclosing, selector.Pos(), "s.db", "direct Store database selector is outside a runtime boundary callback"))
			return true
		})
	}
	return failures
}

func parentMap(root ast.Node) map[ast.Node]ast.Node {
	visitor := &parentVisitor{parents: make(map[ast.Node]ast.Node)}
	ast.Walk(visitor, root)
	return visitor.parents
}

type parentVisitor struct {
	parents map[ast.Node]ast.Node
	stack   []ast.Node
}

func (v *parentVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		v.stack = v.stack[:len(v.stack)-1]
		return nil
	}
	if len(v.stack) > 0 {
		v.parents[node] = v.stack[len(v.stack)-1]
	}
	v.stack = append(v.stack, node)
	return v
}

func calledSelector(call *ast.CallExpr) string {
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
		return selector.Sel.Name
	}
	if ident, ok := call.Fun.(*ast.Ident); ok {
		return ident.Name
	}
	return "<dynamic>"
}

func isRuntimeSQLCall(call *ast.CallExpr, selector string) bool {
	if runtimeSQLSelectors[selector] || runtimeHookSelectors[selector] {
		return true
	}
	return isStoreDBSelectorCall(call)
}

func isStoreDBSelectorCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return isStoreDBSelector(selector.X)
}

func isStoreDBSelector(node ast.Node) bool {
	selector, ok := node.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "db" {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Name == "s"
}

func insideRuntimeBoundary(node ast.Node, parents map[ast.Node]ast.Node) bool {
	for current := node; current != nil; current = parents[current] {
		literal, ok := current.(*ast.FuncLit)
		if !ok {
			continue
		}
		call, ok := parents[literal].(*ast.CallExpr)
		if !ok || !isCallbackArgument(literal, call) {
			continue
		}
		if runtimeBoundaryNames[calledSelector(call)] {
			return true
		}
	}
	return false
}

func isCallbackArgument(literal *ast.FuncLit, call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		if arg == literal {
			return true
		}
	}
	return false
}

func guardFailure(gf guardFile, enclosing string, pos token.Pos, selector, reason string) string {
	position := gf.fset.Position(pos)
	return fmt.Sprintf("%s:%d %s %s: %s", gf.name, position.Line, enclosing, selector, reason)
}
