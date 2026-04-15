// Package config provides a minimal YAML file loader used across quixiot binaries.
//
// The intended precedence is: baked defaults → YAML file → explicit flags (flags win).
// The caller handles the flag overlay because Go's stdlib flag package does not
// give a convenient introspection hook for "was this flag explicitly set". Use
// flag.FlagSet.Visit after Parse to apply the final overlay.
//
// Typical pattern:
//
//	cfg := defaults()
//	if *cfgPath != "" {
//		if err := config.LoadFile(*cfgPath, &cfg); err != nil { ... }
//	}
//	fs.Visit(func(f *flag.Flag) {
//		switch f.Name {
//		case "addr": cfg.Addr = *addrFlag
//		// ...
//		}
//	})
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadFile reads a YAML file and unmarshals it into target. Returns nil if path is empty.
// target must be a non-nil pointer.
func LoadFile(path string, target any) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, target); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	return nil
}
