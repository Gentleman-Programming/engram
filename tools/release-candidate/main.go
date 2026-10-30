// Command release-candidate validates candidate metadata and exports the live
// Go-owned agent contract. It does not establish cross-runtime release readiness.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/mcp"
)

const helpText = `Usage: release-candidate -core-ref refs/tags/vSEMVER -pi-ref refs/tags/pi-vSEMVER [-root directory]

-core-ref  Required full core tag ref with strict SemVer.
-pi-ref    Required full Pi tag ref with independent strict SemVer.
-root      Candidate metadata directory (default: current directory).
-pi-tarball Optional local .tgz to validate against candidate package contents.
-assess    Emit rejected/blocked assessment JSON; never release approval.
-observe-contract Execute the selected trusted core's freshness checker; never approval.
-observe-pi-inputs Also capture trusted selected Pi inputs and verify with selected Go.
-observe-pi-package Bind -pi-tarball bytes to selected Pi source; requires observation.
-observe-artifact-inputs Admit reviewed staging inputs only; never execute Core/Pi.
-observe-artifact-workflows Execute reviewed owned Core lifecycle; Pi workflows unavailable.
-staging-bundle Explicit local #1928 ZIP; requires an artifact observation mode.
-staging-context Reviewed bounded JSON pins; requires an artifact observation mode.
-core-source Full lowercase core commit ID for observation (40 or 64 hex digits).
-pi-source Full lowercase Pi commit ID, independently selected.
-h, --help Show this help.

Reads plugin/pi/package.json and internal/setup/setup.go without writes.
Emits version (core), piVersion (Pi), and the live Go-owned contract.
With -pi-tarball emits both versions, validation=local-package-only, and file count.
Refs define an ordered metadata pair, not remote tags or compatibility proof.
Only literal package file/directory allowlists are supported, up to 4096 files
and 64 MiB. Legacy validation never extracts; links/unexpected/missing content reject.
Artifact-input observation materializes only owned validated Pi bytes, then removes them.
It requires explicit source commits/refs and reviewed context, not publisher authentication.
Missing/mismatched inputs remain non-passing; workflow execution stays unavailable.
Export/validator success is not Pi parity or release approval.
Assessment exits 1 (rejected) or 2 (blocked), with empty stderr.
-assess cannot be combined with -pi-tarball, including an empty archive argument.
Without -assess (or with -assess=false), legacy export/validation is unchanged.
Invocation errors retain plain stderr and exit 1, not assessment JSON.
Observation requires operator-reviewed/trusted source; a copy is NOT a sandbox.
It executes Go from that core commit with local cached dependencies only.
-observe-pi-inputs also executes reviewed Pi/helper code; requires Node24 and
fixed checkout-local TypeBox1.3.34. No installs, stub fallback, or release approval.
-observe-pi-package reuses local archive validation, without executing package code.
Only this explicit opt-in permits -observe-contract with a non-empty -pi-tarball.
Ref/version labels are intents, not verified remote tags. Output always blocks.
No publication, version selection, or bypass flags are supported.
`

var semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		var result *assessmentExit
		if errors.As(err, &result) {
			os.Exit(result.code)
		}
		message := err.Error()
		if errors.Is(err, errPackageRejected) {
			message = "Pi package tarball validation failed"
		}
		fmt.Fprintln(os.Stderr, message)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("release-candidate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	coreRef := flags.String("core-ref", "", "full core tag ref")
	piRef := flags.String("pi-ref", "", "full Pi tag ref")
	root := flags.String("root", ".", "candidate metadata directory")
	tarball := flags.String("pi-tarball", "", "optional local Pi package tarball")
	assess := flags.Bool("assess", false, "non-approving structured assessment")
	observe := flags.Bool("observe-contract", false, "observe trusted selected source; not a sandbox")
	piInputs := flags.Bool("observe-pi-inputs", false, "opt-in selected Pi input projection; requires -observe-contract")
	piPackage := flags.Bool("observe-pi-package", false, "opt-in selected Pi archive binding; requires -observe-contract and -pi-tarball")
	artifactInputs := flags.Bool("observe-artifact-inputs", false, "non-executing staged artifact admission")
	artifactWorkflows := flags.Bool("observe-artifact-workflows", false, "explicit trusted artifact execution; Node24 required")
	bundle := flags.String("staging-bundle", "", "explicit local staging ZIP")
	contextFile := flags.String("staging-context", "", "explicit reviewed staging context JSON")
	coreSource := flags.String("core-source", "", "full core source commit ID")
	piSource := flags.String("pi-source", "", "full Pi source commit ID")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(output, helpText)
			return err
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("positional arguments are not supported")
	}
	tarballProvided, sourceProvided, stagingProvided := false, false, false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "staging-bundle" || option.Name == "staging-context" {
			stagingProvided = true
		}
		if option.Name == "pi-tarball" {
			tarballProvided = true
		}
		if option.Name == "core-source" || option.Name == "pi-source" {
			sourceProvided = true
		}
	})
	artifactMode := *artifactInputs || *artifactWorkflows
	if *artifactInputs && *artifactWorkflows {
		return errors.New("artifact observation modes are mutually exclusive")
	}
	if stagingProvided && !artifactMode {
		return errors.New("staging input flags require -observe-artifact-inputs")
	}
	if artifactMode && (*assess || *observe || *piInputs || *piPackage || tarballProvided) {
		return errors.New("-observe-artifact-inputs cannot be combined with other observation modes, -assess or -pi-tarball")
	}
	if *piInputs && !*observe {
		return errors.New("-observe-pi-inputs requires -observe-contract")
	}
	if *piPackage && !*observe {
		return errors.New("-observe-pi-package requires -observe-contract")
	}
	if *observe && (*assess || (tarballProvided && !*piPackage)) {
		return errors.New("-observe-contract cannot be combined with -assess or -pi-tarball")
	}
	if *piPackage && (!tarballProvided || *tarball == "") {
		return errors.New("-observe-pi-package requires a non-empty -pi-tarball")
	}
	if sourceProvided && !*observe && !artifactMode {
		return errors.New("source commit flags require -observe-contract")
	}
	if (*observe || artifactMode) && (!regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`).MatchString(*coreSource) || !regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`).MatchString(*piSource)) {
		return errors.New("core-source and pi-source must be full lowercase commit IDs")
	}
	if *assess && tarballProvided {
		return errors.New("-assess and -pi-tarball cannot be combined")
	}
	if *coreRef == "" || *piRef == "" {
		return errors.New("core-ref and pi-ref are required")
	}
	version, ok := refVersion(*coreRef, "refs/tags/v")
	if !ok {
		return errors.New("invalid core-ref: expected refs/tags/v<strict-semver>")
	}
	piVersion, ok := refVersion(*piRef, "refs/tags/pi-v")
	if !ok {
		return errors.New("invalid pi-ref: expected refs/tags/pi-v<strict-semver>")
	}
	if artifactMode {
		return observeArtifactInputs(*root, *coreRef, *piRef, version, piVersion, *coreSource, *piSource, *bundle, *contextFile, *artifactWorkflows, output)
	}
	if *observe {
		archive := ""
		if *piPackage {
			archive = *tarball
		}
		return observeContract(*root, *coreRef, *piRef, version, piVersion, *coreSource, *piSource, *piInputs, archive, output)
	}
	if *assess {
		return assessCandidate(*root, version, piVersion, output)
	}
	if err := validateMetadata(*root, piVersion); err != nil {
		return err
	}
	if tarballProvided {
		count, err := validatePackage(*root, piVersion, *tarball)
		if err != nil {
			return err
		}
		result, err := json.Marshal(struct {
			Version    string `json:"version"`
			PiVersion  string `json:"piVersion"`
			Validation string `json:"validation"`
			Files      int    `json:"files"`
		}{version, piVersion, "local-package-only", count})
		if err != nil {
			return err
		}
		_, err = output.Write(append(result, '\n'))
		return err
	}
	contract, err := mcp.AgentContractJSON()
	if err != nil {
		return fmt.Errorf("cannot export live Go agent contract: %w", err)
	}
	// Marshal before writing so validation/encoding failures cannot leak stdout.
	result, err := json.Marshal(struct {
		Version   string          `json:"version"`
		PiVersion string          `json:"piVersion"`
		Contract  json.RawMessage `json:"contract"`
	}{version, piVersion, contract})
	if err != nil {
		return fmt.Errorf("cannot encode candidate export: %w", err)
	}
	_, err = output.Write(append(result, '\n'))
	return err
}

func refVersion(ref, prefix string) (string, bool) {
	if !strings.HasPrefix(ref, prefix) {
		return "", false
	}
	version := strings.TrimPrefix(ref, prefix)
	parts := semverPattern.FindStringSubmatch(version)
	if parts == nil {
		return "", false
	}
	if parts[4] != "" {
		for _, identifier := range strings.Split(parts[4], ".") {
			numeric := true
			for _, char := range identifier {
				if char < '0' || char > '9' {
					numeric = false
					break
				}
			}
			if numeric && len(identifier) > 1 && identifier[0] == '0' {
				return "", false
			}
		}
	}
	return version, true
}

func validateMetadata(root, version string) error {
	return validatePairMetadata(root, root, version)
}

func validatePairMetadata(coreRoot, piRoot, version string) error {
	data, err := os.ReadFile(filepath.Join(piRoot, "plugin", "pi", "package.json"))
	if err != nil {
		return &metadataUnavailable{message: "cannot read plugin/pi/package.json"}
	}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return errors.New("invalid plugin/pi/package.json")
	}
	if manifest.Name != "gentle-engram" {
		return errors.New("package name must be gentle-engram")
	}
	if manifest.Version != version {
		return errors.New("package version must match candidate version")
	}
	data, err = os.ReadFile(filepath.Join(coreRoot, "internal", "setup", "setup.go"))
	if err != nil {
		return &metadataUnavailable{message: "cannot read internal/setup/setup.go"}
	}
	file, err := parser.ParseFile(token.NewFileSet(), "setup.go", data, 0)
	if err != nil {
		return errors.New("invalid internal/setup/setup.go")
	}
	// Inspect real constant declarations rather than matching comments, legacy
	// pins, or arbitrary strings that happen to contain the current pin name.
	count, pin := 0, ""
	for _, declaration := range file.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, spec := range group.Specs {
			value := spec.(*ast.ValueSpec)
			for index, name := range value.Names {
				if name.Name != "piGentleEngramPackage" {
					continue
				}
				count++
				if index < len(value.Values) {
					literal, ok := value.Values[index].(*ast.BasicLit)
					if ok && literal.Kind == token.STRING {
						pin, _ = strconv.Unquote(literal.Value)
					}
				}
			}
		}
	}
	if count != 1 {
		return errors.New("expected exactly one piGentleEngramPackage pin")
	}
	if pin != "npm:gentle-engram@"+version {
		return errors.New("installer pin must match candidate version exactly")
	}
	return nil
}
