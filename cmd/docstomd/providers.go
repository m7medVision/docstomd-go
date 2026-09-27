package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/m7medVision/docstomd-go"
)

// configFile is the optional provider config, under os.UserConfigDir
// ($XDG_CONFIG_HOME or ~/.config on Linux).
const configFile = "docstomd/providers.json"

// config is the optional JSON config of named OCR providers.
type config struct {
	Version int `json:"version"`
	// Default names the provider used without --ocr-provider.
	Default   string                   `json:"default"`
	Providers map[string]providerEntry `json:"providers"`
	// Catalogs are extra local-model catalogs (files or https URLs) passed
	// to the local engine.
	Catalogs []string `json:"catalogs"`
	// path is where the config was read from; relative commands resolve
	// against its directory.
	path string
}

// providerEntry is one external engine.
type providerEntry struct {
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	PageCost *float64          `json:"page_cost"`
	Local    bool              `json:"local"`
}

var reservedProviders = []string{"mistral", "local"}

// loadConfig reads the provider config; a missing file is an empty config.
func loadConfig() (*config, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return &config{}, nil
	}
	path := filepath.Join(dir, configFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &config{}, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := &config{path: path}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported config version %d (want 1)", path, cfg.Version)
	}
	for name, entry := range cfg.Providers {
		if slices.Contains(reservedProviders, name) || strings.HasPrefix(name, "exec:") {
			return nil, fmt.Errorf("%s: provider name %q is reserved", path, name)
		}
		if entry.Command == "" {
			return nil, fmt.Errorf("%s: provider %q has no command", path, name)
		}
		if entry.PageCost != nil && *entry.PageCost < 0 {
			return nil, fmt.Errorf("%s: provider %q has a negative page_cost", path, name)
		}
	}
	return cfg, nil
}

// providerFlags are the convert flags that choose and tune the provider.
type providerFlags struct {
	spec     string
	model    string
	lang     string
	backend  string
	catalogs []string
}

// resolveProvider turns --ocr-provider into a provider. Without the flag
// the config's default is used, else Mistral when MISTRAL_API_KEY is set,
// else the local engine.
func resolveProvider(flags providerFlags, cfg *config) (docstomd.OCRProvider, error) {
	spec := flags.spec
	if spec == "" {
		spec = cfg.Default
	}
	if spec == "" {
		spec = "local"
		if os.Getenv("MISTRAL_API_KEY") != "" {
			spec = "mistral"
		}
	}
	switch spec {
	case "mistral":
		return docstomd.NewMistralProvider(docstomd.MistralOptions{Model: flags.model}), nil
	case "local":
		return docstomd.NewLocalOCRProvider(localArgs(flags, cfg)...), nil
	}
	name, isExec := strings.CutPrefix(spec, "exec:")
	if entry, ok := cfg.Providers[name]; ok {
		return docstomd.NewExternalOCRProvider(cfg.external(name, entry)), nil
	}
	if !isExec {
		return nil, fmt.Errorf("unknown --ocr-provider %q (mistral, local, exec:<path>, or a name from %s)", spec, configFile)
	}
	if name == "" {
		return nil, errors.New("--ocr-provider exec: needs a program path or provider name")
	}
	return docstomd.NewExternalOCRProvider(docstomd.ExternalOCRConfig{Command: name}), nil
}

// localArgs are the local engine's serve flags.
func localArgs(flags providerFlags, cfg *config) []string {
	var args []string
	if flags.model != "" {
		args = append(args, "--model", flags.model)
	}
	if flags.lang != "" {
		args = append(args, "--lang", flags.lang)
	}
	if flags.backend != "" {
		args = append(args, "--backend", flags.backend)
	}
	return append(args, catalogArgs(cfg.Catalogs, flags.catalogs)...)
}

// catalogArgs passes the config's and the command line's catalogs.
func catalogArgs(lists ...[]string) []string {
	var args []string
	for _, list := range lists {
		for _, c := range list {
			args = append(args, "--catalog", c)
		}
	}
	return args
}

func (cfg *config) external(name string, entry providerEntry) docstomd.ExternalOCRConfig {
	command := entry.Command
	if strings.ContainsRune(command, filepath.Separator) && !filepath.IsAbs(command) && cfg.path != "" {
		command = filepath.Join(filepath.Dir(cfg.path), command)
	}
	var env []string
	for key, value := range entry.Env {
		env = append(env, key+"="+value)
	}
	slices.Sort(env)
	return docstomd.ExternalOCRConfig{Name: name, Command: command, Args: entry.Args, Env: env, Cost: entry.PageCost, Local: entry.Local}
}
