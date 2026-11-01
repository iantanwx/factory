package frontend

import (
	"errors"
	"maps"
	"slices"
	"testing"
)

// memTree is a tree held in memory: each tracked path and its bytes.
type memTree map[string]string

func (m memTree) Paths() []string { return slices.Sorted(maps.Keys(m)) }

func (m memTree) Read(p string) ([]byte, error) {
	b, ok := m[p]
	if !ok {
		return nil, errors.New("not tracked")
	}
	return []byte(b), nil
}

// withGo adds the Go files every fixture tree holds.
func withGo(m memTree) memTree {
	m["go.mod"] = "module example.com/x\n"
	m["main.go"] = "package main\n"
	m["internal/x/x.go"] = "package x\n"
	return m
}

// C23: the detector finds each signal of part e, and nothing else.
func TestC23DetectorFindsEachSignalAndNothingElse(t *testing.T) {
	cases := []struct {
		name string
		tree memTree
		want []Signal
	}{
		{"react under dependencies of the root package.json",
			memTree{"package.json": `{"dependencies": {"react": "^18.0.0"}}`},
			[]Signal{{Kind: KindPackage, Path: "package.json", Detail: "react"}}},
		{"vue under devDependencies of apps/web/package.json",
			memTree{"apps/web/package.json": `{"devDependencies": {"vue": "^3.0.0"}}`},
			[]Signal{{Kind: KindPackage, Path: "apps/web/package.json", Detail: "vue"}}},
		{"svelte under peerDependencies",
			memTree{"ui/package.json": `{"peerDependencies": {"svelte": "^4.0.0"}}`},
			[]Signal{{Kind: KindPackage, Path: "ui/package.json", Detail: "svelte"}}},
		{"next",
			memTree{"site/package.json": `{"dependencies": {"next": "14.0.0"}}`},
			[]Signal{{Kind: KindPackage, Path: "site/package.json", Detail: "next"}}},
		{"a .tsx file",
			memTree{"web/src/App.tsx": "export {}\n"},
			[]Signal{{Kind: KindFile, Path: "web/src/App.tsx", Detail: ".tsx"}}},
		{"a .vue file",
			memTree{"src/App.vue": "<template/>\n"},
			[]Signal{{Kind: KindFile, Path: "src/App.vue", Detail: ".vue"}}},
		{"an ios directory",
			memTree{"ios/App/AppDelegate.swift": "import UIKit\n"},
			[]Signal{{Kind: KindMobile, Path: "ios", Detail: "ios"}}},
		{"an android directory below the root",
			memTree{"client/android/app/build.gradle": "apply plugin: 'com.android.application'\n"},
			[]Signal{{Kind: KindMobile, Path: "client/android", Detail: "android"}}},
		{"only Go files", memTree{}, nil},
		{"a package.json that names only express",
			memTree{"package.json": `{"dependencies": {"express": "^4.0.0"}}`}, nil},
		{"a package.json that does not parse",
			memTree{"package.json": `{"dependencies": {"react": `}, nil},
		{"a file named ios", memTree{"tools/ios": "#!/bin/sh\n", "android": "notes\n"}, nil},
		{"a react-dom-only package.json",
			memTree{"package.json": `{"dependencies": {"react-dom": "^18.0.0"}}`}, nil},
		{"react under a key that is none of the three",
			memTree{"package.json": `{"optionalDependencies": {"react": "^18.0.0"}, "name": "react"}`}, nil},
		{"a file named like a package.json",
			memTree{"docs/old-package.json": `{"dependencies": {"react": "^18.0.0"}}`}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Detect(withGo(c.tree))
			if !slices.Equal(got.Signals, c.want) {
				t.Fatalf("signals = %+v, want %+v", got.Signals, c.want)
			}
			if (got.Proposed == nil) != (len(c.want) == 0) {
				t.Fatalf("proposed = %+v with %d signals", got.Proposed, len(c.want))
			}
		})
	}
}

// C23: a package.json that names react in two keys, and next, shows one
// signal for each framework, and the signals sort by path.
func TestC23DetectorNamesEachFrameworkOnceInPathOrder(t *testing.T) {
	got := Detect(withGo(memTree{
		"web/package.json": `{"dependencies": {"react": "1", "next": "1"}, "devDependencies": {"react": "1"}}`,
		"app/App.tsx":      "",
	}))
	want := []Signal{
		{Kind: KindFile, Path: "app/App.tsx", Detail: ".tsx"},
		{Kind: KindPackage, Path: "web/package.json", Detail: "next"},
		{Kind: KindPackage, Path: "web/package.json", Detail: "react"},
	}
	if !slices.Equal(got.Signals, want) {
		t.Fatalf("signals = %+v, want %+v", got.Signals, want)
	}
}

// C24: the proposal names one glob for each package.json directory and each
// mobile directory, and a file glob only for a file no other glob covers.
// Kind is native when a mobile directory shows.
func TestC24DetectProposesABlockThatCoversEachSignal(t *testing.T) {
	cases := []struct {
		name  string
		tree  memTree
		kind  string
		paths []string
	}{
		{"a package under apps/web", memTree{
			"apps/web/package.json": `{"dependencies": {"react": "1"}}`,
			"apps/web/src/App.tsx":  "",
		}, "web", []string{"apps/web/**"}},
		{"a package directory whose name ends in a dot", memTree{
			"apps/web./package.json": `{"dependencies": {"react": "1"}}`,
			"apps/web./App.tsx":      "",
		}, "web", []string{"apps/web./**"}},
		{"the root package covers every other glob", memTree{
			"package.json":          `{"dependencies": {"react": "1"}}`,
			"apps/web/package.json": `{"dependencies": {"vue": "1"}}`,
			"ios/App.swift":         "",
		}, "native", []string{"**"}},
		{"a file outside each package", memTree{
			"apps/web/package.json": `{"dependencies": {"react": "1"}}`,
			"tools/x.tsx":           "",
			"tools/y.vue":           "",
		}, "web", []string{"apps/web/**", "**/*.tsx", "**/*.vue"}},
		{"mobile directories", memTree{
			"ios/App.swift":                 "",
			"android/app/build.gradle":      "",
			"android/app/src/main/View.tsx": "",
		}, "native", []string{"android/**", "ios/**"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Detect(withGo(c.tree)).Proposed
			if got == nil {
				t.Fatal("proposed = nil")
			}
			var paths []string
			for _, g := range got.Paths {
				paths = append(paths, g.String())
			}
			if string(got.Kind) != c.kind || !slices.Equal(paths, c.paths) {
				t.Fatalf("proposed = %s %v, want %s %v", got.Kind, paths, c.kind, c.paths)
			}
		})
	}
}
