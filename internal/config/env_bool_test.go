package config

import (
	"testing"

	"gopkg.in/yaml.v2"
)

func TestEnvBoolNativeBool(t *testing.T) {
	var holder struct {
		V EnvBool `yaml:"v"`
	}
	if err := yaml.Unmarshal([]byte("v: true\n"), &holder); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := holder.V.Resolve(func(s string) (string, error) { return s, nil }); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !holder.V.Value() {
		t.Error("Value() = false, want true")
	}
}

func TestEnvBoolEnvReference(t *testing.T) {
	var holder struct {
		V EnvBool `yaml:"v"`
	}
	if err := yaml.Unmarshal([]byte("v: ${KEMP1_SKIP_VERIFY}\n"), &holder); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := holder.V.Resolve(func(string) (string, error) { return "true", nil }); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !holder.V.Value() {
		t.Error("Value() = false, want true after resolving ${VAR} to \"true\"")
	}
}

func TestEnvBoolAbsentDefaultsFalse(t *testing.T) {
	var holder struct {
		V EnvBool `yaml:"v"`
	}
	if err := yaml.Unmarshal([]byte("{}\n"), &holder); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := holder.V.Resolve(func(s string) (string, error) { return s, nil }); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if holder.V.Value() {
		t.Error("absent insecureSkipVerify resolved true; must default false")
	}
}
