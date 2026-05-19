package orchestrator

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// AgentConfig is the on-disk shape of a single agent definition.
//
// TruncateFromEnd flips the tool-result truncation strategy: leave it
// unset (default) for time-series MCP responses where the newest data
// is at the end and should be preserved; set true to keep the start
// of the response instead.
type AgentConfig struct {
	Name            string   `yaml:"name"`
	Description     string   `yaml:"description"`
	Model           string   `yaml:"model"`
	SystemPrompt    string   `yaml:"system_prompt"`
	MCPTools        []string `yaml:"mcp_tools"`
	TruncateFromEnd bool     `yaml:"truncate_from_end"`
}

// LoadAgents reads a YAML file containing a top-level list of agent
// definitions and returns them as []Agent.
func LoadAgents(path string) ([]Agent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agents config %q: %w", path, err)
	}

	var cfgs []AgentConfig
	if err := yaml.Unmarshal(data, &cfgs); err != nil {
		return nil, fmt.Errorf("parse agents config %q: %w", path, err)
	}

	agents := make([]Agent, len(cfgs))
	for i, cfg := range cfgs {
		agents[i] = Agent{
			Name:            cfg.Name,
			Description:     cfg.Description,
			SystemPrompt:    cfg.SystemPrompt,
			Model:           cfg.Model,
			MCPTools:        cfg.MCPTools,
			TruncateFromEnd: cfg.TruncateFromEnd,
		}
	}
	return agents, nil
}