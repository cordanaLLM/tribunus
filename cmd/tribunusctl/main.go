// Command tribunusctl syncs the Tribunus model catalog from a handful of
// local and remote sources and can render the resulting snapshot as a
// table. See tribunus/catalog for the record shape and
// docs/tribunus/data-sync.md for what each source does and does not know.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "sync":
		err = runSync(os.Args[2:])
	case "show":
		err = runShow(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
		return
	default:
		printUsage()
		err = fmt.Errorf("unknown command: %s", os.Args[1])
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("tribunusctl - Tribunus model catalog data sync")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  tribunusctl sync [--sources=a,b] [--out=file] [--litellm-base=url] [--litellm-token-file=path] [--ollama=url]")
	fmt.Println("  tribunusctl show [--in=file]")
}
