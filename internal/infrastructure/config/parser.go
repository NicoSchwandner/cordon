package config

import (
	"fmt"
	"os"

	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
	"gopkg.in/yaml.v3"
)

// Load reads and validates a .cordon.yaml file.
func Load(path string) (*domain.ProjectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return Parse(data)
}

// Parse parses .cordon.yaml content.
func Parse(data []byte) (*domain.ProjectConfig, error) {
	var cfg domain.ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validate(cfg *domain.ProjectConfig) error {
	if cfg.Version == "" {
		return fmt.Errorf("missing required field: version")
	}
	if cfg.Name == "" {
		return fmt.Errorf("missing required field: name")
	}
	for i, svc := range cfg.Services {
		for _, override := range svc.TierOverrides {
			if override.Tier < 1 || override.Tier > 4 {
				return fmt.Errorf("service %d (%s): tier override has invalid tier %d (must be 1-4)", i, svc.Name, override.Tier)
			}
		}
	}
	return nil
}

// ToTierOverrides converts config overrides to application-layer overrides.
func ToTierOverrides(cfg *domain.ProjectConfig) []ports.TierOverride {
	var overrides []ports.TierOverride
	for _, svc := range cfg.Services {
		for _, o := range svc.TierOverrides {
			overrides = append(overrides, ports.TierOverride{
				Pattern: o.Pattern,
				Tier:    domain.Tier(o.Tier),
			})
		}
	}
	return overrides
}

// ToSecretRefs converts config secrets to domain SecretRefs.
func ToSecretRefs(cfg *domain.ProjectConfig) []domain.SecretRef {
	refs := make([]domain.SecretRef, len(cfg.Secrets))
	for i, s := range cfg.Secrets {
		refs[i] = domain.SecretRef{Name: s.Name, Placeholder: s.Placeholder}
	}
	return refs
}
