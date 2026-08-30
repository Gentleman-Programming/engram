//go:build !windows

package command

import (
	"context"
	"reflect"
	"testing"
)

func TestNewContextPreservesDefaultProcessAttributes(t *testing.T) {
	cmd := NewContext(context.Background(), "git", "status")

	if want := []string{"git", "status"}; !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("command arguments = %q; want %q", cmd.Args, want)
	}
	if cmd.SysProcAttr != nil {
		t.Fatalf("SysProcAttr = %#v; want nil", cmd.SysProcAttr)
	}
}
