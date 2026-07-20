//go:build windows

package project

import (
	"context"
	"reflect"
	"testing"
)

func TestNewProjectCommandContextHidesWindow(t *testing.T) {
	cmd := newProjectCommandContext(context.Background(), "git", "status")

	if want := []string{"git", "status"}; !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("command arguments = %q; want %q", cmd.Args, want)
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil; want Windows process attributes")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("SysProcAttr.HideWindow is false; want true")
	}
}
