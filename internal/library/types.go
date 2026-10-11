// Package library stores reusable declarative capability packs and profiles.
// Import and selection never execute tools, copy credentials, or establish that
// a running harness loaded a capability. Native application is a separate step.
package library

import "context"

const SchemaVersion = 1

type Asset struct {
	ID          string `json:"id"`
	Path        string `json:"path"` // Clean bundle-relative file (rule) or directory (skill).
	Description string `json:"description,omitempty"`
}

type Tool struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Transport   string   `json:"transport"` // stdio or https; no implicit shell.
	Command     []string `json:"command,omitempty"`
	URL         string   `json:"url,omitempty"`
	EnvRefs     []string `json:"env_refs,omitempty"` // Names only; values remain local.
}

type Manifest struct {
	Schema      int               `json:"schema"`
	ID          string            `json:"id"`
	Version     string            `json:"version"`
	Description string            `json:"description,omitempty"`
	Source      string            `json:"source,omitempty"`
	Revision    string            `json:"revision,omitempty"`
	Files       map[string]string `json:"files"` // Exact SHA-256 for every bundled file.
	Skills      []Asset           `json:"skills,omitempty"`
	Rules       []Asset           `json:"rules,omitempty"`
	Tools       []Tool            `json:"tools,omitempty"`
}

type Package struct {
	Manifest Manifest `json:"manifest"`
	Digest   string   `json:"digest"` // Canonical manifest + file hashes; immutable pin.
}

type Selection struct {
	Digest     string          `json:"digest"` // Exact selected immutable package revision.
	Enabled    bool            `json:"enabled"`
	Components map[string]bool `json:"components,omitempty"` // kind/id overrides.
}

type Profile struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	Revision uint64               `json:"revision"`
	Packages map[string]Selection `json:"packages"`
}

type Catalog struct {
	Schema   int       `json:"schema"`
	Present  bool      `json:"present"`
	Revision uint64    `json:"revision"`
	Packages []Package `json:"packages"`
	Profiles []Profile `json:"profiles"`
}

type ResolvedAsset struct {
	PackageID     string `json:"package_id"`
	PackageDigest string `json:"package_digest"`
	Asset         Asset  `json:"asset"`
}

type ResolvedTool struct {
	PackageID     string `json:"package_id"`
	PackageDigest string `json:"package_digest"`
	Tool          Tool   `json:"tool"`
}

type Resolution struct {
	ProfileID       string          `json:"profile_id"`
	ProfileRevision uint64          `json:"profile_revision"`
	CatalogRevision uint64          `json:"catalog_revision"`
	Digest          string          `json:"digest"`
	Packages        []Package       `json:"packages"`
	Skills          []ResolvedAsset `json:"skills"`
	Rules           []ResolvedAsset `json:"rules"`
	Tools           []ResolvedTool  `json:"tools"`
	Loaded          string          `json:"loaded"` // unknown: declaration is not live-process evidence.
}

// Registry is the stable boundary for callers and native configuration adapters.
// The implementation must anchor private files, serialize mutation, preserve
// edited/unowned content and reject a changed immutable pack before resolution.
// Empty reads must not create a directory, identity, lock or database.
type Registry interface {
	Read(context.Context) (Catalog, error)
	Import(context.Context, string) (Package, error)
	CreateProfile(context.Context, string, string) (Profile, error)
	SelectPackage(context.Context, string, string, string, bool) (Profile, error)
	SelectComponent(context.Context, string, string, string, bool) (Profile, error)
	Resolve(context.Context, string) (Resolution, error)
	PackageRoot(context.Context, string, string) (string, error)
	Remove(context.Context, string, string) error
}

// GuardedSelections binds a displayed selection edit to the current profile
// revision under the registry writer lock. A stale UI must refresh instead of
// changing a newer pin or component preference selected by another client.
type GuardedSelections interface {
	SelectPackageAt(context.Context, string, string, string, bool, uint64) (Profile, error)
	SelectComponentAt(context.Context, string, string, string, bool, uint64, string) (Profile, error)
}
