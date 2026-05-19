package orchestrator

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	defaultCoordinatorModel       = "claude-sonnet-4-6"
	defaultCoordinatorRouterModel = "claude-sonnet-4-6"
)

// CoordinatorConfig is the on-disk shape of the coordinator's settings.
// Unset fields fall back to the values from DefaultCoordinatorConfig.
type CoordinatorConfig struct {
	SystemPrompt string `yaml:"system_prompt"`
	Model        string `yaml:"model"`
	RouterModel  string `yaml:"router_model"`
}

// DefaultCoordinatorConfig returns the built-in defaults applied when
// no config file is present or specific fields are left empty.
func DefaultCoordinatorConfig() CoordinatorConfig {
	return CoordinatorConfig{
		SystemPrompt: synthSystemPrompt,
		Model:        defaultCoordinatorModel,
		RouterModel:  defaultCoordinatorRouterModel,
	}
}

// LoadCoordinator reads a YAML coordinator config from path. A missing
// file yields full defaults with no error; any other read or parse
// failure is returned. Individual unset fields are filled in from the
// defaults so partial configs are valid.
func LoadCoordinator(path string) (CoordinatorConfig, error) {
	defaults := DefaultCoordinatorConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return defaults, nil
		}
		return CoordinatorConfig{}, fmt.Errorf("read coordinator config %q: %w", path, err)
	}

	var cfg CoordinatorConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return CoordinatorConfig{}, fmt.Errorf("parse coordinator config %q: %w", path, err)
	}

	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = defaults.SystemPrompt
	}
	if cfg.Model == "" {
		cfg.Model = defaults.Model
	}
	if cfg.RouterModel == "" {
		cfg.RouterModel = defaults.RouterModel
	}
	return cfg, nil
}