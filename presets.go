package main

import "github.com/archerpm/doh-client/internal/config"

// Preset is a group of rules the user can add with one click; the data lives in
// internal/config so a fresh configuration can start with the same rules.
type Preset = config.Preset

var presets = config.Presets()
