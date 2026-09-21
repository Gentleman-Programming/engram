package server

import (
	"os"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/testenv"
)

func TestMain(m *testing.M) {
	restore := testenv.PrependTempDirGitCeiling()
	code := m.Run()
	restore()
	os.Exit(code)
}
