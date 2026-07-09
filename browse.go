package main

// browse.go — a read-only, server-side folder browser powering the path picker.
//
// It only ever lists DIRECTORY NAMES (never file contents), and only for a path
// the operator explicitly navigates to — it does not crawl, follow the tree, or
// enumerate anything without intent. That keeps it from casually exposing what
// lives inside another user's mounts on a shared box: you see a folder's
// immediate subfolders when you choose to open it, and nothing more.

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// browseRoots returns the picker's starting points: the real drives/root, the
// operator's home, the configured staging folder, and each registered source
// root — the places a path is actually likely to live.
func (a *App) browseRoots() []map[string]any {
	out := []map[string]any{}
	seen := map[string]bool{}
	add := func(name, p string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		np := normPath(p)
		if seen[np] {
			return
		}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			seen[np] = true
			out = append(out, map[string]any{"name": name, "path": p})
		}
	}
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			add(string(c)+":\\", string(c)+":\\")
		}
	} else {
		add("/", "/")
	}
	if home, err := os.UserHomeDir(); err == nil {
		add("Home", home)
	}
	cfg := a.LoadConfig()
	add("Staging", cfg.StagingDir)
	for _, r := range a.Store.SourceRoots() {
		add("Source · "+filepath.Base(r), r)
	}
	return out
}

// Browse lists the immediate subdirectories of path (read-only). An empty path
// returns the roots. Dotfiles and OS-junk folders are hidden.
func (a *App) Browse(path string) (map[string]any, error) { return a.browse(path, false) }

// BrowseWithFiles is Browse plus the immediate FILES in the folder — used by the
// "point me at a binary" picker (the Audacity→FFmpeg pattern) so the operator can
// select an executable, not just navigate folders.
func (a *App) BrowseWithFiles(path string) (map[string]any, error) { return a.browse(path, true) }

func (a *App) browse(path string, includeFiles bool) (map[string]any, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]any{"path": "", "parent": "", "roots": true, "dirs": a.browseRoots()}, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	dirs := []map[string]any{}
	files := []map[string]any{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			if skipBrowseDir(name) {
				continue
			}
			dirs = append(dirs, map[string]any{"name": name, "path": filepath.Join(abs, name)})
		} else if includeFiles {
			files = append(files, map[string]any{"name": name, "path": filepath.Join(abs, name)})
		}
	}
	byName := func(l []map[string]any) {
		sort.Slice(l, func(i, j int) bool {
			return strings.ToLower(l[i]["name"].(string)) < strings.ToLower(l[j]["name"].(string))
		})
	}
	byName(dirs)
	byName(files)
	parent := filepath.Dir(abs)
	if normPath(parent) == normPath(abs) {
		parent = "" // already at a drive/root — "up" returns to the roots list
	}
	out := map[string]any{"path": abs, "parent": parent, "dirs": dirs}
	if includeFiles {
		out["files"] = files
	}
	return out, nil
}

// skipBrowseDir hides OS bookkeeping folders that are never a useful target.
func skipBrowseDir(name string) bool {
	switch name {
	case "$RECYCLE.BIN", "System Volume Information", "$WinREAgent", "Config.Msi",
		".Trashes", ".Spotlight-V100", ".fseventsd", "lost+found":
		return true
	}
	return false
}
