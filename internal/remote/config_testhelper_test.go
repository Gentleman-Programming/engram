package remote

import "testing"

// testConfigStore is a simple in-memory implementation of ConfigStore for tests.
type testConfigStore struct {
	data map[string]string
}

func newTestConfigStore(t *testing.T) *testConfigStore {
	t.Helper()
	return &testConfigStore{data: make(map[string]string)}
}

func (s *testConfigStore) GetCloudConfig(key string) string {
	return s.data[key]
}

func (s *testConfigStore) SetCloudConfig(key, value string) error {
	s.data[key] = value
	return nil
}
