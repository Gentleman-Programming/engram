package engram_test

import (
	"encoding/xml"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type signPathStep struct {
	Name     string            `yaml:"name"`
	ID       string            `yaml:"id"`
	Uses     string            `yaml:"uses"`
	Run      string            `yaml:"run"`
	If       string            `yaml:"if"`
	Continue bool              `yaml:"continue-on-error"`
	With     map[string]string `yaml:"with"`
	Env      map[string]string `yaml:"env"`
}
type signPathJob struct {
	If          string            `yaml:"if"`
	RunsOn      string            `yaml:"runs-on"`
	Environment string            `yaml:"environment"`
	Timeout     int               `yaml:"timeout-minutes"`
	Permissions map[string]string `yaml:"permissions"`
	Env         map[string]string `yaml:"env"`
	Steps       []signPathStep    `yaml:"steps"`
}
type signPathWorkflow struct {
	On map[string]struct {
		Paths  []string       `yaml:"paths"`
		Inputs map[string]any `yaml:"inputs"`
	} `yaml:"on"`
	Permissions map[string]string      `yaml:"permissions"`
	Jobs        map[string]signPathJob `yaml:"jobs"`
}

func signPathRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func signPathParse(t *testing.T) (string, signPathWorkflow) {
	t.Helper()
	text := signPathRead(t, ".github/workflows/signpath-test.yml")
	var w signPathWorkflow
	if err := yaml.Unmarshal([]byte(text), &w); err != nil {
		t.Fatal(err)
	}
	return text, w
}
func TestSignPathWorkflowProvenanceAndGates(t *testing.T) {
	text, w := signPathParse(t)
	dispatch, exists := w.On["workflow_dispatch"]
	if !exists || len(w.On) != 2 || len(dispatch.Inputs) != 0 {
		t.Errorf("unexpected triggers: %+v", w.On)
	}
	if len(w.Permissions) != 2 || w.Permissions["contents"] != "read" || w.Permissions["actions"] != "read" {
		t.Errorf("permissions: %v", w.Permissions)
	}
	job, ok := w.Jobs["signpath-test"]
	if !ok || len(w.Jobs) != 2 || job.If != "github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main'" || job.RunsOn != "windows-latest" || job.Environment != "signpath-test" || job.Timeout <= 0 || job.Timeout > 60 || len(job.Permissions) != 0 {
		t.Fatalf("unsafe signing job: %+v", job)
	}
	names := []string{"Checkout", "Set up Go", "Check expected certificate", "Build test binaries", "Test verifier", "Upload unsigned artifact", "Sign with SignPath", "Verify signed artifacts", "Upload verified test artifacts"}
	if len(job.Steps) != len(names) {
		t.Fatalf("unexpected steps: %+v", job.Steps)
	}
	for i, s := range job.Steps {
		if s.Name != names[i] || s.If != "" || s.Continue {
			t.Errorf("step %d bypass or order: %+v", i, s)
		}
		if s.Uses != "" && !regexp.MustCompile(`^[^@]+@[a-f0-9]{40}$`).MatchString(s.Uses) {
			t.Errorf("unpinned: %s", s.Uses)
		}
	}
	if job.Steps[0].With["persist-credentials"] != "false" {
		t.Error("checkout persists credentials")
	}
	for _, want := range []string{"./cmd/engram", "main.version=signpath-test", "-mod=readonly", "-trimpath", "-buildvcs=true", "CGO_ENABLED", "GOOS", "GOARCH", "amd64", "arm64", "RUNNER_TEMP", "LASTEXITCODE", "vcs.modified=false", "vcs.revision="} {
		if !strings.Contains(job.Steps[3].Run, want) {
			t.Errorf("build missing %q", want)
		}
	}
	unsigned, sign, verified := job.Steps[5], job.Steps[6], job.Steps[8]
	if unsigned.ID != "unsigned" || !strings.HasPrefix(unsigned.Uses, "actions/upload-artifact@") || unsigned.With["if-no-files-found"] != "error" {
		t.Error("unsigned artifact ID missing")
	}
	want := map[string]string{"api-token": "${{ secrets.SIGNPATH_API_TOKEN }}", "organization-id": "538456f4-515b-43d4-99a7-0e75f7825d00", "project-slug": "engram", "signing-policy-slug": "test-signing", "artifact-configuration-slug": "github-test-zip", "github-artifact-id": "${{ steps.unsigned.outputs.artifact-id }}", "wait-for-completion": "true", "output-artifact-directory": "${{ runner.temp }}\\signpath-signed"}
	if sign.Uses != "SignPath/github-action-submit-signing-request@f6d04783b4569d051e0c80105fe66e82819d0092" || len(sign.With) != len(want) {
		t.Errorf("unexpected sign action: %+v", sign)
	}
	for k, v := range want {
		if sign.With[k] != v {
			t.Errorf("sign input %s = %q", k, sign.With[k])
		}
	}
	if strings.Count(text, "secrets.SIGNPATH_API_TOKEN") != 1 || job.Steps[2].Env["EXPECTED_CERTIFICATE_SHA256"] != "${{ vars.SIGNPATH_TEST_CERTIFICATE_SHA256 }}" || !strings.Contains(job.Steps[2].Run, "64}") {
		t.Error("secret or certificate validation scope")
	}
	if !strings.Contains(job.Steps[4].Run, "verify-signpath-test.tests.ps1") || !strings.Contains(job.Steps[7].Run, "verify-signpath-test.ps1") || job.Steps[7].Env["EXPECTED_CERTIFICATE_SHA256"] != "${{ vars.SIGNPATH_TEST_CERTIFICATE_SHA256 }}" {
		t.Error("fixture or signed output verification missing")
	}
	if !strings.HasPrefix(verified.Uses, "actions/upload-artifact@") || !strings.Contains(verified.With["path"], "signpath-signed") || verified.With["if-no-files-found"] != "error" {
		t.Error("verified upload not scoped")
	}
}
func TestSignPathPremergeFixturesAreSecretFree(t *testing.T) {
	_, w := signPathParse(t)
	pr, ok := w.On["pull_request"]
	if !ok || len(pr.Paths) == 0 {
		t.Fatal("missing PR paths")
	}
	for _, p := range []string{".github/workflows/signpath-test.yml", ".github/signpath/github-test-zip.xml", "scripts/verify-signpath-test.ps1", "scripts/verify-signpath-test.tests.ps1", "signpath_test.go", "docs/signpath-test.md", "go.mod", "go.sum", "cmd/engram/**", "internal/**"} {
		found := false
		for _, got := range pr.Paths {
			if got == p {
				found = true
			}
		}
		if !found {
			t.Errorf("missing path %s", p)
		}
	}
	job := w.Jobs["premerge-fixtures"]
	if job.If != "github.event_name == 'pull_request'" || job.RunsOn != "windows-latest" || job.Environment != "" || job.Timeout <= 0 || job.Timeout > 60 || len(job.Permissions) != 0 || len(job.Env) != 0 || len(job.Steps) != 4 {
		t.Fatalf("unsafe PR job: %+v", job)
	}
	for _, s := range job.Steps {
		if s.If != "" || s.Continue || len(s.Env) != 0 || strings.Contains(strings.ToLower(s.Run), "secrets.") || strings.Contains(strings.ToLower(s.Uses), "signpath/") || strings.Contains(strings.ToLower(s.Uses), "upload-artifact") {
			t.Errorf("unsafe PR step: %+v", s)
		}
		for _, value := range s.With {
			if strings.Contains(strings.ToLower(value), "secrets.") || strings.Contains(strings.ToLower(value), "github.token") {
				t.Errorf("PR token: %+v", s)
			}
		}
		if s.Uses != "" && !regexp.MustCompile(`^[^@]+@[a-f0-9]{40}$`).MatchString(s.Uses) {
			t.Errorf("unpinned: %s", s.Uses)
		}
	}
	if job.Steps[0].With["persist-credentials"] != "false" || !strings.Contains(job.Steps[3].Run, "verify-signpath-test.tests.ps1") {
		t.Error("missing safe checkout or fixtures")
	}
	for _, want := range []string{"./cmd/engram", "main.version=signpath-test", "-mod=readonly", "-trimpath", "-buildvcs=true", "CGO_ENABLED", "GOOS", "GOARCH", "amd64", "arm64", "RUNNER_TEMP", "LASTEXITCODE"} {
		if !strings.Contains(job.Steps[2].Run, want) {
			t.Errorf("PR build missing %q", want)
		}
	}
}
func TestSignPathArtifactConfiguration(t *testing.T) {
	var c struct {
		XMLName xml.Name
		Zip     struct {
			Files []struct {
				Path string     `xml:"path,attr"`
				Sign []struct{} `xml:"authenticode-sign"`
			} `xml:"pe-file"`
		} `xml:"zip-file"`
	}
	if err := xml.Unmarshal([]byte(signPathRead(t, ".github/signpath/github-test-zip.xml")), &c); err != nil {
		t.Fatal(err)
	}
	if c.XMLName.Space != "http://signpath.io/artifact-configuration/v1" || c.XMLName.Local != "artifact-configuration" || len(c.Zip.Files) != 2 {
		t.Fatalf("invalid config: %+v", c)
	}
	seen := map[string]bool{}
	for _, f := range c.Zip.Files {
		if len(f.Sign) != 1 || seen[f.Path] {
			t.Errorf("invalid PE entry: %+v", f)
		}
		seen[f.Path] = true
	}
	for _, p := range []string{"engram-windows-amd64.exe", "engram-windows-arm64.exe"} {
		if !seen[p] {
			t.Errorf("missing %s", p)
		}
	}
}
func TestSignPathPreservesReleaseStaging(t *testing.T) {
	staging := signPathRead(t, ".github/workflows/stage-release-candidate.yml")
	for _, want := range []string{"permissions:\n  contents: read", "persist-credentials: false", "-mod=readonly -trimpath -buildvcs=true", "./cmd/engram", "vcs.modified=false"} {
		if !strings.Contains(staging, want) {
			t.Errorf("staging behavior missing %q", want)
		}
	}
	if strings.Contains(staging, "SIGNPATH_API_TOKEN") || strings.Contains(staging, "SignPath/github-action") {
		t.Error("test signing must not enter release staging")
	}
}

func TestSignPathVerifierIsFailClosed(t *testing.T) {
	v := signPathRead(t, "scripts/verify-signpath-test.ps1")
	for _, want := range []string{"Get-AuthenticodeSignature", "SignerCertificate", "SHA256", "RawData", "NotTrusted", "CustomRootTrust", "CustomTrustStore", "NoCheck", "finally", "0x8664", "0xAA64", "ReparsePoint", "SignatureType", "HashMismatch", "SIGNPATH-STAGE"} {
		if !strings.Contains(v, want) {
			t.Errorf("verifier missing %q", want)
		}
	}
	for _, banned := range []string{"LocalMachine", "X509Store", "certutil", "Cert:\\", ".Remove("} {
		if strings.Contains(v, banned) {
			t.Errorf("verifier must not use %q", banned)
		}
	}
	fixture := signPathRead(t, "scripts/verify-signpath-test.tests.ps1")
	for _, want := range []string{"tampered", "wrong signer", "unsigned", "missing", "extra", "architecture", "intact", "NonExportable", "WaitForExit", "Kill($true)", "timed out", "SIGNPATH-STAGE", "Verifier leaked trust anchor"} {
		if !strings.Contains(fixture, want) {
			t.Errorf("fixture missing %q", want)
		}
	}
	if strings.Contains(fixture, "$store.Add(") || strings.Contains(fixture, "preexisting anchor") {
		t.Error("fixtures must not add trust anchors")
	}
}
