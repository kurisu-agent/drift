// Package config is drift's YAML configuration loader.
//
// Two schemas live here, one per binary:
//
//   - [Client] — `~/.config/drift/config.yaml` on a workstation. Path resolved
//     via [ClientConfigPath], which honors XDG_CONFIG_HOME.
//   - [Server] — `~/.drift/garage/config.yaml` on a circuit. Garage root
//     resolved via [GarageDir], which honors $HOME.
//
// Both types are decoded with strict YAML parsing (unknown keys rejected) and
// validated via [Client.Validate] / [Server.Validate]. [WriteFileAtomic] is
// the single file-write primitive callers should use — it writes to a sibling
// temp file and renames into place so readers never observe a half-written
// file.
package config
