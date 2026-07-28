package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/nkaewam/mrstack/internal/homebrew"
)

func main() {
	var formulaPath string
	var checksumsPath string
	var tag string

	flag.StringVar(&formulaPath, "formula", "", "path to the Homebrew formula")
	flag.StringVar(&checksumsPath, "checksums", "", "path to SHA256SUMS")
	flag.StringVar(&tag, "tag", "", "release tag, such as v0.1.0")
	flag.Parse()

	if formulaPath == "" || checksumsPath == "" || tag == "" {
		fmt.Fprintln(os.Stderr, "usage: update-homebrew-formula -formula PATH -checksums PATH -tag TAG")
		os.Exit(2)
	}

	formula, err := os.ReadFile(formulaPath)
	if err != nil {
		fatalf("read formula: %v", err)
	}
	checksums, err := os.ReadFile(checksumsPath)
	if err != nil {
		fatalf("read checksums: %v", err)
	}
	updated, err := homebrew.UpdateFormula(formula, tag, checksums)
	if err != nil {
		fatalf("update formula: %v", err)
	}
	if err := os.WriteFile(formulaPath, updated, 0o644); err != nil {
		fatalf("write formula: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
