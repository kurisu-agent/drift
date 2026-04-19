// Package config is drift's YAML configuration loader.
//
// Two schemas, one per binary:
//   - [Client] — `~/.config/drift/config.yaml` (XDG_CONFIG_HOME aware).
//   - [Server] — `~/.drift/garage/config.yaml` ($HOME aware).
//
// Both decode with strict YAML parsing (unknown keys rejected). Writers
// use [WriteFileAtomic] (tmp + rename) so readers never see partial state.
package config
