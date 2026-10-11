package main

import (
	"fmt"
	"io"

	"github.com/tekmora/epitrop/catalog"
)

func runSchema(w io.Writer) error {
	if _, err := w.Write(catalog.SnapshotSchema()); err != nil {
		return fmt.Errorf("write snapshot schema: %w", err)
	}
	return nil
}
