package config

import (
	"fmt"
	"strconv"
	"strings"
)

// EnvBool is a YAML boolean that also accepts a "${VAR}" reference, resolved after
// load. It exists so insecureSkipVerify can be driven from the environment in a
// compose or systemd deployment without templating the config file.
//
// The zero value is false, which is the required default: TLS verification stays on
// unless something explicitly turns it off.
type EnvBool struct {
	raw      string
	resolved bool
}

// UnmarshalYAML accepts either a native bool or a string (possibly a ${VAR} ref).
func (e *EnvBool) UnmarshalYAML(unmarshal func(any) error) error {
	var b bool
	if err := unmarshal(&b); err == nil {
		e.resolved = b
		return nil
	}
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("insecureSkipVerify must be a boolean or a ${VAR} reference: %w", err)
	}
	e.raw = s
	return nil
}

// Resolve expands any ${VAR} reference using interp and parses the result.
// Called once during Load; a no-op when the YAML held a native bool.
func (e *EnvBool) Resolve(interp func(string) (string, error)) error {
	if e.raw == "" {
		return nil
	}
	expanded, err := interp(e.raw)
	if err != nil {
		return err
	}
	expanded = strings.TrimSpace(expanded)
	if expanded == "" {
		return nil
	}
	b, err := strconv.ParseBool(expanded)
	if err != nil {
		return fmt.Errorf("value %q is not a boolean", expanded)
	}
	e.resolved = b
	return nil
}

// Value reports the resolved boolean.
func (e EnvBool) Value() bool { return e.resolved }
