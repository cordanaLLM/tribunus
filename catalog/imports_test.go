package catalog

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// maxCatalogSourceFiles bounds the files the import check reads (HISS-02).
const maxCatalogSourceFiles = 64

// TestCatalogImportsStdlibOnly keeps catalog a package other modules can pin:
// Praetor reads the snapshot through it, so it imports only the standard
// library, never this module's internal packages or a third-party module.
func TestCatalogImportsStdlibOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list catalog sources: %v", err)
	}
	if len(files) > maxCatalogSourceFiles {
		t.Fatalf("catalog has %d Go files, more than the %d this check reads", len(files), maxCatalogSourceFiles)
	}
	fset := token.NewFileSet()
	for i := 0; i < len(files) && i < maxCatalogSourceFiles; i++ {
		if strings.HasSuffix(files[i], "_test.go") {
			continue
		}
		src, err := os.ReadFile(files[i])
		if err != nil {
			t.Fatalf("read %s: %v", files[i], err)
		}
		parsed, err := parser.ParseFile(fset, files[i], src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", files[i], err)
		}
		for _, spec := range parsed.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote import %s: %v", files[i], spec.Path.Value, err)
			}
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %q; catalog must import only the standard library", files[i], path)
			}
		}
	}
}
