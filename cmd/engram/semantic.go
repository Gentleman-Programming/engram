package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/Gentleman-Programming/engram/v3/internal/embedding"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Capture machine configuration once; optional configuration must not interfere
// with lexical startup. Construction and validation wait for an explicit operation.
func configureSemantic(cfg store.Config) store.Config {
	if cfg.EmbeddingProvider == nil && strings.TrimSpace(os.Getenv("ENGRAM_EMBEDDING_MODEL")) != "" {
		cfg.EmbeddingProvider = &lazyEmbedding{config: embedding.Config{Model: os.Getenv("ENGRAM_EMBEDDING_MODEL"), BaseURL: os.Getenv("ENGRAM_EMBEDDING_URL")}}
	}
	return cfg
}

type lazyEmbedding struct {
	config embedding.Config
	once   sync.Once
	client *embedding.Client
	err    error
}

func (p *lazyEmbedding) load() (*embedding.Client, error) {
	p.once.Do(func() { p.client, p.err = embedding.New(p.config) })
	return p.client, p.err
}
func (p *lazyEmbedding) Resolve(ctx context.Context) (embedding.Identity, error) {
	c, err := p.load()
	if err != nil {
		return embedding.Identity{}, err
	}
	return c.Resolve(ctx)
}
func (p *lazyEmbedding) Embed(ctx context.Context, id embedding.Identity, texts []string) ([][]float64, error) {
	c, err := p.load()
	if err != nil {
		return nil, err
	}
	return c.Embed(ctx, id, texts)
}

const embedUsage = "usage: engram embed [--project PROJECT|--all] [--scope SCOPE] [--type TYPE]"

func parseEmbedArgs(args []string) (store.SearchOptions, bool, error) {
	var opts store.SearchOptions
	all := false
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if flag == "--all" {
			all = true
			continue
		}
		if flag != "--project" && flag != "--scope" && flag != "--type" {
			return opts, all, fmt.Errorf("unknown embed flag: %s; %s", flag, embedUsage)
		}
		if i+1 == len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "--") {
			return opts, all, fmt.Errorf("%s requires a value", flag)
		}
		i++
		switch flag {
		case "--project":
			opts.Project = args[i]
		case "--scope":
			opts.Scope = args[i]
		case "--type":
			opts.Type = args[i]
		}
	}
	if all && opts.Project != "" {
		return opts, all, fmt.Errorf("--project cannot be combined with --all")
	}
	return opts, all, nil
}
func cmdEmbed(cfg store.Config) {
	opts, all, err := parseEmbedArgs(os.Args[2:])
	if err != nil {
		fatal(err)
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer func() { _ = s.Close() }()
	opts.Project, err = resolveCLIProjectScope(s, opts.Project, all, true)
	if err != nil {
		fatal(err)
		return
	}
	result, err := s.BuildSemanticIndex(context.Background(), opts)
	if err != nil {
		fatal(fmt.Errorf("%w; configure ENGRAM_EMBEDDING_MODEL and ENGRAM_EMBEDDING_URL, then run engram embed with the same filters", err))
		return
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err)
	}
}
