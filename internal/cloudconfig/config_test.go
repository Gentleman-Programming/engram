package cloudconfig

import "testing"

func TestClearPersistsZeroValueConfig(t *testing.T) {
	dataDir := t.TempDir()
	if err := Save(dataDir, &Config{ServerURL: "https://cloud.example.test", Token: "file-token"}); err != nil {
		t.Fatalf("seed cloud config: %v", err)
	}

	if err := Clear(dataDir); err != nil {
		t.Fatalf("clear cloud config: %v", err)
	}

	got, err := Load(dataDir)
	if err != nil {
		t.Fatalf("load cleared cloud config: %v", err)
	}
	if got.ServerURL != "" || got.Token != "" {
		t.Fatalf("cleared config = %+v, want empty persisted values", got)
	}
}
