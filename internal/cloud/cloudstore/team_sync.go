package cloudstore

import (
	"fmt"
	"strings"
)

// ─── Project Enrollment (server-side team sync) ─────────────────────────────

// EnrollProject registers a user's enrollment in a project on the server.
// Idempotent — re-enrolling is a no-op (updates enrolled_at timestamp).
func (cs *CloudStore) EnrollProject(userID, project string) error {
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("cloudstore: project must not be empty")
	}
	_, err := cs.db.Exec(
		`INSERT INTO cloud_project_enrollments (user_id, project)
		 VALUES ($1, $2)
		 ON CONFLICT (user_id, project) DO UPDATE SET enrolled_at = NOW()`,
		userID, project,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: enroll project: %w", err)
	}
	return nil
}

// UnenrollProject removes a user's enrollment from a project on the server.
// Idempotent — unenrolling a non-enrolled project is a no-op.
func (cs *CloudStore) UnenrollProject(userID, project string) error {
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("cloudstore: project must not be empty")
	}
	_, err := cs.db.Exec(
		`DELETE FROM cloud_project_enrollments WHERE user_id = $1 AND project = $2`,
		userID, project,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: unenroll project: %w", err)
	}
	return nil
}

// SyncEnrollments replaces a user's entire enrollment list atomically.
// This is called when the client pushes its full enrollment list.
func (cs *CloudStore) SyncEnrollments(userID string, projects []string) error {
	tx, err := cs.db.Begin()
	if err != nil {
		return fmt.Errorf("cloudstore: begin sync enrollments: %w", err)
	}
	defer tx.Rollback()

	// Clear existing enrollments for this user.
	if _, err := tx.Exec(`DELETE FROM cloud_project_enrollments WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("cloudstore: clear enrollments: %w", err)
	}

	// Insert new enrollments.
	for _, project := range projects {
		project = strings.TrimSpace(project)
		if project == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO cloud_project_enrollments (user_id, project) VALUES ($1, $2)
			 ON CONFLICT (user_id, project) DO NOTHING`,
			userID, project,
		); err != nil {
			return fmt.Errorf("cloudstore: insert enrollment %q: %w", project, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: commit sync enrollments: %w", err)
	}
	return nil
}

// ListEnrolledProjects returns the projects a user is enrolled in.
func (cs *CloudStore) ListEnrolledProjects(userID string) ([]string, error) {
	rows, err := cs.db.Query(
		`SELECT project FROM cloud_project_enrollments WHERE user_id = $1 ORDER BY project ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list enrolled projects: %w", err)
	}
	defer rows.Close()

	var projects []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("cloudstore: scan enrollment: %w", err)
		}
		projects = append(projects, p)
	}
	if projects == nil {
		projects = []string{}
	}
	return projects, rows.Err()
}

// ─── Cross-User Pull (team sync) ────────────────────────────────────────────

// PullMutationsWithTeamSync returns mutations for a user including cross-user
// observation mutations from teammates enrolled in the same projects.
//
// Rules:
//   - Always returns the user's own mutations (sessions, observations, prompts)
//   - Also returns observation mutations from other users who share enrolled projects
//   - Excludes observations with scope='personal' from other users
//   - Excludes sessions and prompts from other users (those are per-developer workflow)
//   - Falls back to user-only mode if no enrollment data exists
func (cs *CloudStore) PullMutationsWithTeamSync(userID string, sinceSeq int64, limit int) (*PullMutationsResult, error) {
	if limit <= 0 {
		limit = 100
	}

	// Check if the enrollment table has any data for this user.
	hasEnrollments, err := cs.userHasEnrollments(userID)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: check enrollments: %w", err)
	}
	if !hasEnrollments {
		// Fall back to user-only pull if no enrollment data exists on the server.
		return cs.PullMutations(userID, sinceSeq, limit)
	}

	var mutations []CloudMutation
	lastSeq := sinceSeq
	hasMore := false

	for len(mutations) < limit+1 {
		// Combined query: own mutations + cross-user observation mutations.
		// The query uses a UNION to merge:
		//   1. All mutations from the requesting user (same as current behavior)
		//   2. Observation-entity mutations from OTHER users who share enrolled projects,
		//      excluding scope='personal'
		// JOIN cloud_users to get the username for author attribution on cross-user mutations.
		rows, err := cs.db.Query(
			`SELECT cm.seq, cm.user_id,
			        CASE WHEN cm.user_id != $1 THEN COALESCE(cu.username, '') ELSE '' END AS author,
			        cm.entity, cm.entity_key, cm.op, cm.payload, cm.occurred_at
			 FROM cloud_mutations cm
			 LEFT JOIN cloud_users cu ON cu.id = cm.user_id
			 WHERE cm.seq > $2 AND (
			   -- Own mutations (all entities)
			   cm.user_id = $1
			   OR
			   -- Cross-user: observation mutations from teammates on shared projects
			   (
			     cm.user_id != $1
			     AND cm.entity = 'observation'
			     AND EXISTS (
			       SELECT 1 FROM cloud_project_enrollments AS my
			       WHERE my.user_id = $1
			         AND my.project = (cm.payload->>'project')
			         AND EXISTS (
			           SELECT 1 FROM cloud_project_enrollments AS their
			           WHERE their.user_id = cm.user_id
			             AND their.project = my.project
			         )
			     )
			     -- Exclude personal-scoped observations from other users
			     AND COALESCE(cm.payload->>'scope', 'project') != 'personal'
			   )
			 )
			 ORDER BY cm.seq ASC
			 LIMIT $3`,
			userID, lastSeq, limit+1,
		)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: pull team mutations: %w", err)
		}

		fetched := 0
		for rows.Next() {
			fetched++
			var m CloudMutation
			if err := rows.Scan(&m.Seq, &m.UserID, &m.Author, &m.Entity, &m.EntityKey, &m.Op, &m.Payload, &m.OccurredAt); err != nil {
				rows.Close()
				return nil, fmt.Errorf("cloudstore: scan team mutation: %w", err)
			}
			lastSeq = m.Seq

			// Check project sync controls (paused projects).
			project, err := projectFromMutation(m.Entity, m.Payload)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("cloudstore: pull project from team mutation: %w", err)
			}
			enabled, err := cs.IsProjectSyncEnabled(project)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if !enabled {
				continue
			}

			mutations = append(mutations, m)
			if len(mutations) > limit {
				hasMore = true
				break
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("cloudstore: pull team mutations rows: %w", err)
		}
		rows.Close()
		if hasMore || fetched < limit+1 {
			break
		}
	}

	if len(mutations) > limit {
		mutations = mutations[:limit]
	}
	if mutations == nil {
		mutations = []CloudMutation{}
	}
	return &PullMutationsResult{Mutations: mutations, HasMore: hasMore}, nil
}

// userHasEnrollments returns true if the user has at least one project enrollment
// on the server. Used to determine whether team sync should be active.
func (cs *CloudStore) userHasEnrollments(userID string) (bool, error) {
	var count int
	err := cs.db.QueryRow(
		`SELECT COUNT(*) FROM cloud_project_enrollments WHERE user_id = $1`,
		userID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

