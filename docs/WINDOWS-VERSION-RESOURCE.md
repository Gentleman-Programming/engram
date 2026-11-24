# Preparatory Windows version resources

For maintainers preparing disposable build fixtures for [#1703](https://github.com/Gentleman-Programming/engram/issues/1703).
This build-only helper is not connected to Engram builds or release workflows.
It neither signs executables nor establishes Smart App Control admission.

From the repository root, with the Go toolchain declared in `go.mod`:

```sh
go run ./tools/windows-version-resource generate -tag v3.7.2 -out /absolute/disposable-parent/stage
```

The parent directory must exist; `stage` must not exist. Generation refuses any
existing destination, including stale resources and symlinks, without overwriting
it. A write failure may leave an incomplete directory: use a new disposable
destination for a retry. The only outputs are `rsrc_windows_amd64.syso` and
`rsrc_windows_arm64.syso`. Do not generate or copy these into `cmd/engram`, commit
them, or use this preparation to change release artifacts.

To exercise linkage, put a synthetic `package main` Go program and a fixture-only
`go.mod` into the generated staging directory. Build the package with
`GOOS=windows` and each `GOARCH=amd64` / `arm64`, using `CGO_ENABLED=0`. On
PowerShell, set and restore those process environment variables instead of using
POSIX inline assignments. The tests automate the isolated builds on each host.

Read the resulting file without executing it:

```sh
go run ./tools/windows-version-resource verify -tag v3.7.2 -arch amd64 -file /absolute/disposable-parent/fixture-amd64.exe
go test ./tools/windows-version-resource -count=1
```

## Supported contract

- Only canonical stable `vMAJOR.MINOR.PATCH` tags, without whitespace, leading
  zeroes, prerelease or build suffixes. Each numeric component is 0–65535.
  This limitation belongs to the preparatory helper; production prerelease
  behavior remains unchanged.
- `ProductName=engram`, `OriginalFilename=engram.exe`, string `FileVersion` and
  `ProductVersion` equal to the tag without `v`. Fixed file/product versions
  contain the same three unsigned 16-bit components and a fourth component of 0.
- One COFF `.rsrc` section and one architecture-specific ADDR32NB relocation.
  The Go linker selects the object using its `_windows_amd64` or
  `_windows_arm64` suffix; Linux builds exclude both objects.
- One neutral-language resource tree: `RT_VERSION(16)/#1/0`, one en-US Unicode
  `040904B0` string table with the four fields above, and matching translation.
  No icon, manifest, additional string, language or resource is supported.

The encoder uses only the standard library and the documented Microsoft
[PE/COFF](https://learn.microsoft.com/en-us/windows/win32/debug/pe-format) and
[VERSIONINFO](https://learn.microsoft.com/en-us/windows/win32/menurc/vs-versioninfo)
formats. There is no new build-tool or runtime dependency.

## Verification boundaries

The verifier accepts linked PE32+ executable images up to 128 MiB with the exact
expected machine (`0x8664` or `0xaa64`). It checks the resource directory's
location, raw/virtual section and image extents, canonical tree/payload, fixed and string values and zero
section padding. Additional localized metadata, duplicate entries or keys,
fixed/string disagreements and unsupported shapes fail rather than being
interpreted permissively. It deliberately rejects other valid resource layouts.

Canonical payload comparison shares encoder assumptions. To check interoperability
independently, tests build actual disposable Go fixtures for both architectures
and, on Windows, read strings and fixed versions using Windows
`System.Diagnostics.FileVersionInfo` through PowerShell. The fixtures are never
executed. On other systems these OS reads are unavailable and explicitly logged;
portable PE checks and the Linux exclusion build still run. PowerShell is required
for these independent reads on Windows; failure does not silently skip them.

Tests compare a resource-free baseline with a resource-bearing build and reject
wrong/stale versions, machines, missing/duplicate/localized entries, duplicate
string keys, modified fixed/string values, invalid RVAs/extents and truncation.
This is metadata evidence, not reproduction of SAC rejection on current Engram.
The helper does not validate all PE structures, detect arbitrary executable
tampering, verify Authenticode, certificate identity, timestamps or provenance,
or authorize publication. Provider choice, signing integration and qualification
on an SAC-enabled device remain separate work.
The size cap limits input bytes; `debug/pe` is not a hardened parser for arbitrary
hostile files. Use this helper only on locally built, disposable staging artifacts.
