// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"flag"
	"fmt"
	"os"

	assets "enclave"
	"enclave/internal/legacyassets"
)

func main() {
	root := flag.String("root", "", "legacy Enclave asset root")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve home directory: %v\n", err)
		os.Exit(1)
	}
	result, err := legacyassets.Clean(*root, home, assets.FS())
	if err != nil {
		fmt.Fprintf(os.Stderr, "clean legacy Enclave assets: %v\n", err)
		os.Exit(1)
	}
	if result.SkipReason != "" {
		fmt.Printf("Legacy asset cleanup skipped: %s\n", result.SkipReason)
		return
	}
	fmt.Printf("Removed %d known legacy asset entries from %s\n", result.Removed, *root)
}
