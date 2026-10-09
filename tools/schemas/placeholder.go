//go:build !schemasrefresh

// SPDX-FileCopyrightText: 2026 cordanaLLM contributors
//
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "the schema refresh tool builds with: go run -tags schemasrefresh ./tools/schemas")
	os.Exit(1)
}
