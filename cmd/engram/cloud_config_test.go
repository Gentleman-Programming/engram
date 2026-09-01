package main

import (
	"github.com/Gentleman-Programming/engram/internal/cloudconfig"
	"github.com/Gentleman-Programming/engram/internal/store"
)

type cloudConfig = cloudconfig.Config

func loadCloudConfig(cfg store.Config) (*cloudConfig, error) {
	return cloudconfig.Load(cfg.DataDir)
}

func saveCloudConfig(cfg store.Config, config *cloudConfig) error {
	return cloudconfig.Save(cfg.DataDir, config)
}
