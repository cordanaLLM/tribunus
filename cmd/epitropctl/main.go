// Command epitropctl syncs the model data Epitrop routes on from a handful
// of local and remote sources into one snapshot file, and can render that
// snapshot as a table. See the catalog package for the record shape and
// docs/data-sync.md for installing epitropctl and for what each source does
// and does not know.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/tekmora/epitrop/internal/egressproxy"
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
	case egressproxy.RelayCommand:
		os.Exit(egressproxy.RunRelay(os.Args[1:], os.Stderr))
	case "sync":
		err = runSync(os.Args[2:])
	case "jobs":
		err = runJobs(os.Args[2:])
	case "watch":
		err = runWatch(os.Args[2:])
	case "units":
		err = runUnits(os.Args[2:])
	case "graph":
		err = runGraph(os.Args[2:])
	case "effects":
		err = runEffects(os.Args[2:])
	case "slot":
		err = runSlot(os.Args[2:])
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
	fmt.Println("epitropctl - model data sync for the Epitrop graph router")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  epitropctl sync [--sources=a,b] [--out=file] [--litellm-base=url] [--litellm-token-file=path] [--ollama=url]")
	fmt.Println("  epitropctl show [--in=file]")
	fmt.Println("  epitropctl schema")
	fmt.Println("  epitropctl version")
	fmt.Println("  epitropctl jobs start|status|stop|supervise --config=file [name]")
	fmt.Println("  epitropctl watch run|seed --config=file [--once] [--interval=duration]")
	fmt.Println("  epitropctl units set|resume|show|note|inbox --config=file ...")
	fmt.Println("  epitropctl graph show|apply --config=file [--file=stage.json]")
	fmt.Println("  epitropctl effects list|resolve --config=file [--outcome=done|retry --by=name <task id> <action type>]")
	fmt.Println("  epitropctl slot run --config=template --base=rev --brief=file --workspace=dir --out=dir [--repo=dir] [--resume=file] [--decisions=file] <unit id>")
}

func printVersion() {
	if version == "" {
		fmt.Println("dev")
		return
	}
	fmt.Println(version)
}
