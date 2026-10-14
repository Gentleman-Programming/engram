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
-pi-ref    Required full Pi tag ref with the exact same version.
-root      Candidate metadata directory (default: current directory).
-pi-tarball Optional local .tgz to validate against candidate package contents.
-h, --help Show this help.

Reads plugin/pi/package.json and internal/setup/setup.go without writes.
Emits one JSON object containing version and the live Go-owned contract.
With -pi-tarball emits version, validation=local-package-only, and file count.
Only literal package file/directory allowlists are supported, up to 4096 files
and 64 MiB. No archive extraction; links and unexpected/missing content reject.
Export/validator success is not Pi parity or release approval.
No publication, version selection, or bypass flags are supported.
`

var semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
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
	if version != piVersion {
		return errors.New("core and Pi versions must match exactly")
	}
	if err := validateMetadata(*root, version); err != nil {
		return err
	}
	tarballProvided := false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "pi-tarball" {
			tarballProvided = true
		}
	})
	if tarballProvided {
		count, err := validatePackage(*root, version, *tarball)
		if err != nil {
			return err
		}
		result, err := json.Marshal(struct {
			Version    string `json:"version"`
			Validation string `json:"validation"`
			Files      int    `json:"files"`
		}{version, "local-package-only", count})
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
		Version  string          `json:"version"`
		Contract json.RawMessage `json:"contract"`
	}{version, contract})
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
	data, err := os.ReadFile(filepath.Join(root, "plugin", "pi", "package.json"))
	if err != nil {
		return errors.New("cannot read plugin/pi/package.json")
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
	data, err = os.ReadFile(filepath.Join(root, "internal", "setup", "setup.go"))
	if err != nil {
		return errors.New("cannot read internal/setup/setup.go")
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
