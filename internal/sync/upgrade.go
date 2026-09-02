package sync

import (
	"fmt"
	"strings"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

const (
	UpgradeStatusReady   = constants.UpgradeStatusReady
	UpgradeStatusBlocked = constants.UpgradeStatusBlocked

	UpgradeReasonClassReady      = constants.UpgradeClassReady
	UpgradeReasonClassRepairable = constants.UpgradeClassRepairable
	UpgradeReasonClassBlocked    = constants.UpgradeClassBlocked
	UpgradeReasonClassPolicy     = constants.UpgradeClassPolicy

	UpgradeReasonReady                 = constants.UpgradeReasonReady
	UpgradeReasonRepairableUnenrolled  = constants.UpgradeReasonRepairableUnenrolled
	UpgradeReasonBlockedProjectMissing = constants.UpgradeReasonBlockedProjectMissing
	UpgradeReasonPolicyConfig          = constants.UpgradeReasonPolicyConfig
	UpgradeReasonPolicyForbidden       = constants.UpgradeReasonPolicyForbidden
)

type UpgradeDiagnosisInput struct {
	Project         string
	CloudConfigured bool
	ProjectEnrolled bool
	PolicyDenied    bool
}

type UpgradeDiagnosisReport struct {
	Status  string
	Class   string
	Code    string
	Message string
}

func DiagnoseCloudUpgrade(input UpgradeDiagnosisInput) (UpgradeDiagnosisReport, error) {
	project, _ := store.NormalizeProject(input.Project)
	project = strings.TrimSpace(project)
	if project == "" {
		return UpgradeDiagnosisReport{}, fmt.Errorf("cloud upgrade project is required")
	}
	if input.PolicyDenied {
		return UpgradeDiagnosisReport{
			Status:  UpgradeStatusBlocked,
			Class:   UpgradeReasonClassPolicy,
			Code:    UpgradeReasonPolicyForbidden,
			Message: fmt.Sprintf("project %q is blocked by organization policy", project),
		}, nil
	}
	if !input.CloudConfigured {
		return UpgradeDiagnosisReport{
			Status:  UpgradeStatusBlocked,
			Class:   UpgradeReasonClassPolicy,
			Code:    UpgradeReasonPolicyConfig,
			Message: "cloud configuration is required before upgrade bootstrap",
		}, nil
	}
	if !input.ProjectEnrolled {
		return UpgradeDiagnosisReport{
			Status:  UpgradeStatusBlocked,
			Class:   UpgradeReasonClassRepairable,
			Code:    UpgradeReasonRepairableUnenrolled,
			Message: fmt.Sprintf("project %q is not enrolled for cloud sync yet", project),
		}, nil
	}
	return UpgradeDiagnosisReport{
		Status:  UpgradeStatusReady,
		Class:   UpgradeReasonClassReady,
		Code:    UpgradeReasonReady,
		Message: fmt.Sprintf("project %q is ready for cloud bootstrap", project),
	}, nil
}
