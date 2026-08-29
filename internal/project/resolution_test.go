package project

import (
	"errors"
	"testing"
)

func TestResolveModes(t *testing.T) {
	t.Run("current uses a valid process override before cwd", func(t *testing.T) {
		result, err := Resolve(ResolutionOptions{
			Mode:            ResolutionCurrent,
			ProcessOverride: " Trusted Project ",
			Directory:       t.TempDir(),
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if result.Project != "trusted project" || result.Source != SourceProcessOverride {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("explicit canonicalizes repeated separators before checking the store", func(t *testing.T) {
		result, err := Resolve(ResolutionOptions{
			Mode:                 ResolutionCurrent,
			Explicit:             "Foo--Bar",
			RequireKnownExplicit: true,
			ProjectExists:        func(name string) (bool, error) { return name == "foo-bar", nil },
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if result.Project != "foo-bar" || result.Source != SourceExplicitOverride {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("process override canonicalizes repeated separators before checking the store", func(t *testing.T) {
		result, err := Resolve(ResolutionOptions{
			Mode:                ResolutionCurrent,
			ProcessOverride:     "Foo__Bar",
			RequireKnownProcess: true,
			ProjectExists:       func(name string) (bool, error) { return name == "foo_bar", nil },
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if result.Project != "foo_bar" || result.Source != SourceProcessOverride {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("process override rejects paths", func(t *testing.T) {
		_, err := Resolve(ResolutionOptions{Mode: ResolutionCurrent, ProcessOverride: `C:\\repo`})
		if !errors.Is(err, ErrInvalidProjectName) {
			t.Fatalf("error = %v; want ErrInvalidProjectName", err)
		}
	})

	t.Run("known override is required when configured", func(t *testing.T) {
		_, err := Resolve(ResolutionOptions{
			Mode:                ResolutionCurrent,
			ProcessOverride:     "missing",
			RequireKnownProcess: true,
			ProjectExists:       func(string) (bool, error) { return false, nil },
		})
		var unknown *UnknownProjectError
		if !errors.As(err, &unknown) || unknown.Name != "missing" {
			t.Fatalf("error = %T %v; want unknown missing", err, err)
		}
	})

	t.Run("explicit validates independently", func(t *testing.T) {
		result, err := Resolve(ResolutionOptions{
			Mode:                 ResolutionCurrent,
			Explicit:             "Known",
			ProcessOverride:      "ignored",
			RequireKnownExplicit: true,
			ProjectExists:        func(name string) (bool, error) { return name == "known", nil },
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if result.Project != "known" || result.Source != SourceExplicitOverride {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("all remains deliberately global", func(t *testing.T) {
		result, err := Resolve(ResolutionOptions{Mode: ResolutionAll, Explicit: "ignored", ProcessOverride: "ignored"})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if result.Project != "" || result.Source != SourceAllProjects {
			t.Fatalf("result = %+v", result)
		}
	})
}
