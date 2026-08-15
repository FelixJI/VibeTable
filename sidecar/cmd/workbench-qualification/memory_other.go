//go:build !windows

package main

import "runtime"

func processMemory() (uint64, uint64, error) {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	// VibeTable ships only on Windows. This fallback keeps source-level Go
	// checks portable; release qualification executes the Windows RSS path.
	return memory.Sys, memory.HeapSys, nil
}
