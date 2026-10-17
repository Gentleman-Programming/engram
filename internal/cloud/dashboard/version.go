package dashboard

// buildVersionSuffix renders the running build version as dashboard chrome.
//
// The value is rendered verbatim rather than normalized: "dev" is a legitimate
// value for local builds, and an empty version is deliberately not replaced by a
// placeholder because the absence of a version is itself diagnostic. An empty
// value contributes no suffix at all so the footer never ends in a dangling
// separator.
func buildVersionSuffix(version string) string {
	if version == "" {
		return ""
	}
	return " · " + version
}
