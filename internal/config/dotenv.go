package config

import (
	"path/filepath"

	"github.com/joho/godotenv"
)

// LoadDotEnv loads a .env file from the working directory and then from the config
// file's directory, before any ${VAR} interpolation runs.
//
// godotenv never overrides an already-set environment variable, so real secret
// injection (systemd EnvironmentFile, compose environment:, a Kubernetes secret)
// always wins over a checked-out .env. Missing files are not an error — .env is a
// developer convenience, and config.yaml remains the source of truth.
func LoadDotEnv(cfgPath string) {
	_ = godotenv.Load(".env")
	if cfgPath != "" {
		_ = godotenv.Load(filepath.Join(filepath.Dir(cfgPath), ".env"))
	}
}
