package graphene

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const defaultBranchPrefix = "stack"

type Config struct {
	BranchPrefix string
}

type diskConfig struct {
	BranchPrefix *string `json:"branchPrefix"`
}

func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{BranchPrefix: defaultBranchPrefix}
	path := configPath(getenv)
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var disk diskConfig
	if err := json.Unmarshal(data, &disk); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if disk.BranchPrefix != nil {
		cfg.BranchPrefix = *disk.BranchPrefix
	}
	return cfg, nil
}

func configPath(getenv func(string) string) string {
	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "graphene", "config.json")
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "graphene", "config.json")
	}
	return ""
}
