package config

import (
	"fmt"
	"strings"
)

// SafeConfig renders the configuration for logging with every credential removed.
// Use it anywhere the config would otherwise be printed; the raw Config must never
// reach a log line.
func SafeConfig(c *Config) string {
	if c == nil {
		return "<nil config>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "server: %s:%s%s\n", c.Server.Host, c.Server.Port, c.Server.URI)
	fmt.Fprintf(&b, "collection: interval=%s timeout=%s maxConcurrent=%d\n",
		c.Collection.Interval, c.Collection.Timeout, c.Collection.MaxConcurrent)
	fmt.Fprintf(&b, "otel: enabled=%t endpoint=%s insecure=%t interval=%s\n",
		c.OTel.Enabled, c.OTel.Endpoint, c.OTel.Insecure, c.OTel.Interval)
	for _, s := range c.Systems {
		fmt.Fprintf(&b, "system %s: url=%s auth=%s insecureSkipVerify=%t\n",
			s.Name, s.BaseURL(), authMode(s), s.InsecureSkipVerify.Value())
	}
	return b.String()
}

// authMode describes which credentials are present without revealing them.
func authMode(s System) string {
	switch {
	case s.APIKey != "" && s.Username != "":
		return "apikey+session"
	case s.APIKey != "":
		return "apikey"
	case s.Username != "":
		return "session"
	default:
		return "none"
	}
}
