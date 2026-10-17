package dashboard

import "testing"

func TestBuildVersionSuffix(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{
			name:    "tagged release is rendered verbatim",
			version: "v1.20.3",
			want:    " · v1.20.3",
		},
		{
			name:    "dev build is rendered verbatim rather than dropped",
			version: "dev",
			want:    " · dev",
		},
		{
			name:    "empty version contributes no suffix",
			version: "",
			want:    "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildVersionSuffix(tc.version); got != tc.want {
				t.Fatalf("buildVersionSuffix(%q) = %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}
