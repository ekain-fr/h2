package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Users map[string]*UserConfig `yaml:"users"`
}

type UserConfig struct {
	Bridges BridgesConfig `yaml:"bridges"`
}

type BridgesConfig struct {
	Telegram    *TelegramConfig    `yaml:"telegram"`
	MacOSNotify *MacOSNotifyConfig `yaml:"macos_notify"`
}

type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ChatID   int64  `yaml:"chat_id"`
}

type MacOSNotifyConfig struct {
	Enabled bool `yaml:"enabled"`
}

// ConfigDir returns the h2 configuration directory (~/.h2/).
func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".h2")
	}
	return filepath.Join(home, ".h2")
}

// Load reads the h2 config from ~/.h2/config.yaml.
// If the file does not exist, it returns an empty Config with no error.
func Load() (*Config, error) {
	return LoadFrom(filepath.Join(ConfigDir(), "config.yaml"))
}

// LoadFrom reads the h2 config from the given path.
// If the file does not exist, it returns an empty Config with no error.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
