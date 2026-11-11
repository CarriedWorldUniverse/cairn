package main

import (
	"fmt"
	"os"
	"runtime/pprof"
	"runtime/trace"
)

// startProfile is a hidden diagnostics hook: when $CAIRN_CPUPROFILE names a
// file, the whole command runs under the CPU profiler and the profile is
// written there (read it with `go tool pprof`); $CAIRN_TRACE does the same for
// an execution trace (`go tool trace`), which is what shows where a command
// WAITS rather than computes. Both are off — and cost nothing — unless set.
// The returned stop function must run before the process exits; main calls it
// ahead of os.Exit, which would otherwise skip a deferred stop and truncate
// the profile.
func startProfile() (stop func()) {
	var stops []func()
	if path := os.Getenv("CAIRN_CPUPROFILE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cairn: CAIRN_CPUPROFILE:", err)
		} else if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, "cairn: CAIRN_CPUPROFILE:", err)
			_ = f.Close()
		} else {
			stops = append(stops, func() { pprof.StopCPUProfile(); _ = f.Close() })
		}
	}
	if path := os.Getenv("CAIRN_TRACE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cairn: CAIRN_TRACE:", err)
		} else if err := trace.Start(f); err != nil {
			fmt.Fprintln(os.Stderr, "cairn: CAIRN_TRACE:", err)
			_ = f.Close()
		} else {
			stops = append(stops, func() { trace.Stop(); _ = f.Close() })
		}
	}
	return func() {
		for _, s := range stops {
			s()
		}
	}
}
