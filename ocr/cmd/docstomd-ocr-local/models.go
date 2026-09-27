package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/m7medVision/docstomd-go/ocr/catalog"
)

// resolveModel picks the model directory: --model-dir, else the catalog
// selection rule over installed models.
func resolveModel(ctx context.Context, opts options, stderr io.Writer) (string, error) {
	if opts.modelDir != "" {
		if _, err := os.Stat(filepath.Join(opts.modelDir, "manifest.json")); err != nil {
			return "", fmt.Errorf("%w: no model manifest in %s", errUnavailable, opts.modelDir)
		}
		return opts.modelDir, nil
	}
	set, err := catalog.LoadSet(ctx, opts.catalogs)
	if err != nil {
		// Installed models must work offline: fall back to the built-in
		// catalog's order when a user catalog cannot be read.
		fmt.Fprintf(stderr, "docstomd-ocr-local: %v; using the built-in catalog order\n", err)
		set = catalog.Set{catalog.Load()}
	}
	dir, err := catalog.Select(set, modelsRoot(opts.modelsRoot), opts.model, opts.lang)
	if errors.Is(err, catalog.ErrNotInstalled) {
		return "", fmt.Errorf("%w: %v", errUnavailable, err)
	}
	return dir, err
}

func modelsRoot(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return catalog.Root()
}

// catalogFlags registers the flags every catalog subcommand shares.
func catalogFlags(fs *flag.FlagSet) (root *string, catalogs *stringList) {
	root = fs.String("models", "", "directory holding installed models (default $DOCSTOMD_OCR_MODELS, else the user data directory)")
	catalogs = &stringList{}
	fs.Var(catalogs, "catalog", "extra model catalog (file or https URL); repeatable")
	return root, catalogs
}

type listEntry struct {
	ID        string   `json:"id"`
	Family    string   `json:"family"`
	Languages []string `json:"languages"`
	License   string   `json:"license"`
	Size      int64    `json:"size"`
	Catalog   string   `json:"catalog"`
	Installed bool     `json:"installed"`
}

func runList(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docstomd-ocr-local list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root, catalogs := catalogFlags(fs)
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	set, err := catalog.LoadSet(ctx, *catalogs)
	if err != nil {
		fmt.Fprintln(stderr, "docstomd-ocr-local:", err)
		return 1
	}
	installed := map[string]bool{}
	for _, id := range catalog.Installed(modelsRoot(*root)) {
		installed[id] = true
	}
	var entries []listEntry
	for _, e := range set.Entries() {
		entries = append(entries, listEntry{ID: e.ID, Family: e.Family, Languages: e.Languages, License: e.License, Size: e.Size(), Catalog: e.Origin, Installed: installed[e.ID]})
		delete(installed, e.ID)
	}
	for id := range installed {
		entries = append(entries, listEntry{ID: id, Catalog: "(not in any catalog)", Installed: true})
	}
	if *jsonOut {
		if err := json.NewEncoder(stdout).Encode(entries); err != nil {
			return 1
		}
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLANGUAGES\tSIZE\tLICENSE\tINSTALLED")
	for _, e := range entries {
		mark := ""
		if e.Installed {
			mark = "yes"
		}
		size := ""
		if e.Size > 0 {
			size = catalog.HumanSize(e.Size)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.ID, strings.Join(e.Languages, ","), size, e.License, mark)
	}
	if err := tw.Flush(); err != nil {
		return 1
	}
	fmt.Fprintf(stdout, "\nmodels directory: %s\n", modelsRoot(*root))
	return 0
}

func runInstall(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("docstomd-ocr-local install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root, catalogs := catalogFlags(fs)
	var accept stringList
	fs.Var(&accept, "accept-license", "accept a restricted licence (SPDX id, or model id); repeatable")
	if err := fs.Parse(reorder(args, map[string]bool{"--models": true, "--catalog": true, "--accept-license": true, "-models": true, "-catalog": true, "-accept-license": true})); err != nil {
		return 1
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "docstomd-ocr-local install: name at least one model id (see: list)")
		return 1
	}
	set, err := catalog.LoadSet(ctx, *catalogs)
	if err != nil {
		fmt.Fprintln(stderr, "docstomd-ocr-local:", err)
		return 1
	}
	for _, id := range fs.Args() {
		e, ok := set.Find(id)
		if !ok {
			fmt.Fprintf(stderr, "docstomd-ocr-local: no model %q in the catalogs (see: list)\n", id)
			return 1
		}
		opts := catalog.InstallOptions{Root: modelsRoot(*root), AcceptLicenses: accept, Progress: stderr}
		if err := catalog.Install(ctx, e, opts); err != nil {
			fmt.Fprintln(stderr, "docstomd-ocr-local:", err)
			return 1
		}
		fmt.Fprintf(stderr, "%s: installed in %s\n", id, filepath.Join(opts.Root, id))
	}
	return 0
}

func runCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docstomd-ocr-local check-catalog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	_, catalogs := catalogFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	set, err := catalog.LoadSet(ctx, *catalogs)
	if err != nil {
		fmt.Fprintln(stderr, "docstomd-ocr-local:", err)
		return 1
	}
	failed := false
	for _, e := range set.Entries() {
		if err := catalog.Check(ctx, nil, e); err != nil {
			fmt.Fprintln(stderr, "FAIL", err)
			failed = true
			continue
		}
		fmt.Fprintln(stdout, "ok  ", e.ID)
	}
	if failed {
		return 1
	}
	return 0
}

// reorder moves flags before positional arguments so they may follow ids.
func reorder(args []string, takesValue map[string]bool) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if takesValue[a] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		rest = append(rest, a)
	}
	return append(flags, rest...)
}
