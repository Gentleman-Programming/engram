package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Gentleman-Programming/engram/v3/internal/diagnostic"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

const doctorHostSessionUsage = "usage: engram doctor host-session --pid PID [--json]"

func cmdDoctorHostSession(cfg store.Config, args []string) {
	flags := flag.NewFlagSet("engram doctor host-session", flag.ContinueOnError)
	flags.Usage = func() { _, _ = fmt.Fprintln(flags.Output(), doctorHostSessionUsage) }
	pid := flags.Int("pid", 0, "live local Claude host PID")
	jsonOut := flags.Bool("json", false, "write the diagnostic report as JSON")
	if err := flags.Parse(args); err != nil {
		if err != flag.ErrHelp {
			exitFunc(1)
		}
		return
	}
	if *pid <= 1 || flags.NArg() != 0 {
		_, _ = fmt.Fprintln(os.Stderr, doctorHostSessionUsage)
		exitFunc(1)
		return
	}

	report := diagnostic.LiveHostSessionReport(context.Background(), cfg.DataDir, *pid)
	if *jsonOut {
		writeDoctorJSON(report)
		return
	}
	renderDoctorText(report)
}
