package cloudstore

import (
	"context"
	"fmt"
)

// CreateProject creates a new project. Returns the project ID.
func (s *Store) CreateProject(ctx context.Context, name string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		"INSERT INTO projects (name) VALUES ($1) RETURNING id", name,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create project: %w", err)
	}
	return id, nil
}

// AddMember adds a user to a project with the given role (owner/admin/member).
func (s *Store) AddMember(ctx context.Context, projectName, userEmail, role string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO project_members (project_id, user_id, role)
		SELECT p.id, u.id, $3
		FROM projects p, users u
		WHERE p.name = $1 AND u.email = $2
		ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role
	`, projectName, userEmail, role)
	if err != nil {
		return fmt.Errorf("add member: %w", err)
	}
	return nil
}

// RemoveMember removes a user from a project.
func (s *Store) RemoveMember(ctx context.Context, projectName, userEmail string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM project_members
		WHERE project_id = (SELECT id FROM projects WHERE name = $1)
		  AND user_id = (SELECT id FROM users WHERE email = $2)
	`, projectName, userEmail)
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	return nil
}

// IsMember checks if a user is a member of a project.
func (s *Store) IsMember(ctx context.Context, projectName, userID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM project_members pm
			JOIN projects p ON p.id = pm.project_id
			WHERE p.name = $1 AND pm.user_id = $2
		)
	`, projectName, userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check membership: %w", err)
	}
	return exists, nil
}

// ListUserProjects returns project names the user is a member of.
func (s *Store) ListUserProjects(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.name FROM projects p
		JOIN project_members pm ON pm.project_id = p.id
		WHERE pm.user_id = $1
		ORDER BY p.name
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
