package main

import (
	"context"
	"embed"
	"errors"
	"os"
	"path/filepath"

	"github.com/seanheiney/keelmux/internal/library"
)

//go:embed presets/headroom/README.md presets/headroom/keelmux-plugin.json
var pluginPresets embed.FS

// A built-in is a declarative local bundle. Extraction and Import never install
// upstream executables or start them; health/application remain explicit.
func importPreset(ctx context.Context, r library.Registry, name string) (library.Package, error) {
	if name != "headroom" {
		return library.Package{}, errors.New("known built-in pack: headroom")
	}
	dir, err := os.MkdirTemp("", "keelmux-preset-")
	if err != nil {
		return library.Package{}, err
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return library.Package{}, err
	}
	for _, file := range []string{"README.md", "keelmux-plugin.json"} {
		b, err := pluginPresets.ReadFile("presets/" + name + "/" + file)
		if err != nil {
			return library.Package{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, file), b, 0600); err != nil {
			return library.Package{}, err
		}
	}
	return r.Import(ctx, dir)
}
