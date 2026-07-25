// Package config persists spruce's user preferences across runs — currently
// just the brew ask-mode consent choice. Small enough to keep as one file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is spruce's persisted user preferences.
type Config struct {
	// BrewAutoConfirm means the user chose "don't ask again" when spruce
	// offered to suppress brew's install/upgrade confirmation prompts.
	BrewAutoConfirm bool `json:"brew_auto_confirm"`
}

// Path returns the config file location: $SPRUCE_CONFIG_FILE if set, else
// os.UserConfigDir()/spruce/config.json.
func Path() (string, error) {
	if p := os.Getenv("SPRUCE_CONFIG_FILE"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("Resolve config directory: %w", err)
	}
	return filepath.Join(dir, "spruce", "config.json"), nil
}

// Load reads the config file. A missing file is not an error — it returns a
// zero-value Config.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("Read config file: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("Parse config file: %w", err)
	}
	return cfg, nil
}

// Save writes the config file atomically (temp file + rename), creating its
// parent directory if needed.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("Create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("Encode config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("Write config file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("Rename config file: %w", err)
	}
	return nil
}
