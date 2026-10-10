// Command tribunusctl syncs the model data Tribunus routes on from a handful
// of local and remote sources into one snapshot file, and can render that
// snapshot as a table. See the catalog package for the record shape and
// docs/data-sync.md for installing tribunusctl and for what each source does
// and does not know.
package main

import (
	"errors"
	"fmt"
	"os"
)

var version = ""

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "__job-shim":
		os.Exit(runJobShim(os.Args[1:]))
	case "sync":
		err = runSync(os.Args[2:])
	case "jobs":
		err = runJobs(os.Args[2:])
	case "watch":
		err = runWatch(os.Args[2:])
	case "units":
		err = runUnits(os.Args[2:])
	case "show":
		err = runShow(os.Args[2:])
	case "schema":
		err = runSchema(os.Stdout)
	case "version":
		printVersion()
	case "help", "-h", "--help":
		printUsage()
		return
	default:
		printUsage()
		err = fmt.Errorf("unknown command: %s", os.Args[1])
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		var exitCoder interface{ ExitCode() int }
		if errors.As(err, &exitCoder) {
			os.Exit(exitCoder.ExitCode())
		}
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("tribunusctl - model data sync for the Tribunus graph router")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  tribunusctl sync [--sources=a,b] [--out=file] [--litellm-base=url] [--litellm-token-file=path] [--ollama=url]")
	fmt.Println("  tribunusctl show [--in=file]")
	fmt.Println("  tribunusctl schema")
	fmt.Println("  tribunusctl version")
	fmt.Println("  tribunusctl jobs start|status|stop|supervise --config=file [name]")
	fmt.Println("  tribunusctl watch run|seed --config=file [--once] [--interval=duration]")
	fmt.Println("  tribunusctl units set|resume|show|note|inbox --config=file ...")
}

func printVersion() {
	if version == "" {
		fmt.Println("dev")
		return
	}
	fmt.Println(version)
}
