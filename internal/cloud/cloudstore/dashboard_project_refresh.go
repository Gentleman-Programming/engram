package cloudstore

import (
	"sort"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/chunkcodec"
)

// Invalidation retains only an immutable, permission-neutral baseline. Readers
// still wait for fresh content; no deleted or replaced content is served stale.
func (cs *CloudStore) invalidateDashboardProjects(projects ...string) {
	if cs == nil {
		return
	}
	cs.dashboardReadModelMu.Lock()
	defer cs.dashboardReadModelMu.Unlock()
	if !cs.dashboardReadModel.projectPartitionSafe {
		cs.dashboardReadModel = dashboardReadModel{}
		cs.dashboardReadModelDirty = nil
	} else {
		if cs.dashboardReadModelDirty == nil {
			cs.dashboardReadModelDirty = make(map[string]struct{})
		}
		for _, project := range projects {
			cs.dashboardReadModelDirty[project] = struct{}{}
		}
	}
	cs.dashboardReadModelOK = false
	cs.dashboardReadModelGen++
	cs.dashboardReadModelBuild = nil
}

func dashboardMutationIsProjectLocal(project, payload string) bool {
	if project == "" || strings.TrimSpace(project) != project {
		return false
	}
	var body struct {
		Project *string `json:"project"`
	}
	if err := chunkcodec.DecodeSyncMutationPayload(payload, &body); err != nil {
		return false
	}
	return body.Project == nil || resolveProjectValue(*body.Project, project) == project
}

func dashboardChunkIsProjectLocal(chunk dashboardChunkRow) bool {
	project := chunk.project
	if project == "" || strings.TrimSpace(project) != project {
		return false
	}
	for _, row := range chunk.parsed.Sessions {
		if resolveProjectValue(row.Project, project) != project {
			return false
		}
	}
	for _, row := range chunk.parsed.Observations {
		if row.Project != nil && resolveProjectValue(*row.Project, project) != project {
			return false
		}
	}
	for _, row := range chunk.parsed.Prompts {
		if resolveProjectValue(row.Project, project) != project {
			return false
		}
	}
	for _, row := range chunk.parsed.Mutations {
		if !dashboardMutationIsProjectLocal(project, row.Payload) {
			return false
		}
	}
	return true
}

func (cs *CloudStore) refreshDashboardProjects(base dashboardReadModel, projects []string) (dashboardReadModel, error) {
	if !base.projectPartitionSafe || len(projects) == 0 {
		return cs.buildDashboardReadModel()
	}
	sort.Strings(projects)
	replacements := make(map[string]DashboardProjectDetail, len(projects))
	controls, err := cs.ListProjectSyncControls()
	if err != nil {
		return dashboardReadModel{}, err
	}
	for _, project := range projects {
		if project == "" || strings.TrimSpace(project) != project {
			return cs.buildDashboardReadModel()
		}
		fold := newDashboardReadModelFold()
		if err := cs.streamDashboardChunkRowsForProject(project, fold.applyChunk); err != nil {
			return dashboardReadModel{}, err
		}
		if err := cs.streamDashboardMutationRowsForProject(project, fold.applyMutation); err != nil {
			return dashboardReadModel{}, err
		}
		// A new mixed-project payload invalidates the partition certificate. Rebuild
		// globally before returning, since its effects can cross the dirty boundary.
		if !fold.projectPartitionSafe {
			return cs.buildDashboardReadModel()
		}
		part := fold.finish()
		for _, control := range controls {
			if strings.TrimSpace(control.Project) == project {
				part = part.withRegisteredProjects([]ProjectSyncControl{control})
			}
		}
		if detail, ok := part.projectDetails[project]; ok {
			replacements[project] = detail
		}
	}
	return mergeDashboardProjectDetails(base, projects, replacements), nil
}

func mergeDashboardProjectDetails(base dashboardReadModel, projects []string, replacements map[string]DashboardProjectDetail) dashboardReadModel {
	// Copy the map; nested slices are immutable snapshot data and are only read.
	details := make(map[string]DashboardProjectDetail, len(base.projectDetails)+len(replacements))
	for project, detail := range base.projectDetails {
		details[project] = detail
	}
	for _, project := range projects {
		delete(details, project)
	}
	for project, detail := range replacements {
		details[project] = detail
	}
	model := dashboardReadModel{
		projectPartitionSafe: true,
		projectDetails:       details,
		projects:             make([]DashboardProjectRow, 0, len(details)),
		contributors:         make([]DashboardContributorRow, 0),
	}
	contributors := make(map[string]DashboardContributorRow)
	for _, detail := range details {
		model.projects = append(model.projects, detail.Stats)
		model.admin.Chunks += detail.Stats.Chunks
		for _, part := range detail.Contributors {
			row := contributors[part.CreatedBy]
			row.CreatedBy = part.CreatedBy
			row.Chunks += part.Chunks
			row.Projects++
			if parseRFC3339(part.LastChunkAt).After(parseRFC3339(row.LastChunkAt)) {
				row.LastChunkAt = part.LastChunkAt
			}
			contributors[part.CreatedBy] = row
		}
	}
	for _, row := range contributors {
		model.contributors = append(model.contributors, row)
	}
	sort.Slice(model.projects, func(i, j int) bool { return model.projects[i].Project < model.projects[j].Project })
	sort.Slice(model.contributors, func(i, j int) bool {
		if model.contributors[i].Chunks != model.contributors[j].Chunks {
			return model.contributors[i].Chunks > model.contributors[j].Chunks
		}
		return model.contributors[i].CreatedBy < model.contributors[j].CreatedBy
	})
	model.admin.Projects = len(model.projects)
	model.admin.Contributors = len(model.contributors)
	return model
}
