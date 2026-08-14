//go:build integration

// This file, together with ./integration/cgroup_container_test.go, makes
// up the container integration test described in CONTRIBUTING.md. It has
// to live here rather than in integration/ because it needs access to
// package main's unexported readCgroupVal/readCgroupCPULimit — the same
// functions collectFrame in render.go calls on every tick. It's excluded
// from the default `go test ./...` run by the integration build tag.
//
// It has no assertions of its own: when compiled with `go test -c -tags
// integration` and run with BOXTOP_CGROUP_DUMP=1 set, TestMain below
// skips the normal test machinery and instead prints what this code
// detects as JSON on stdout, so the driver test in ./integration can run
// that binary inside a container and check the result on the host.
package main

import (
	"encoding/json"
	"os"
	"testing"
)

// TestMain lets this compiled test binary double as the in-container
// "worker" that dumps detected cgroup limits, instead of running the usual
// test suite, whenever BOXTOP_CGROUP_DUMP=1 is set in its environment.
func TestMain(m *testing.M) {
	if os.Getenv("BOXTOP_CGROUP_DUMP") == "1" {
		dumpCgroupLimits()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// cgroupDump is the JSON shape written to stdout. Kept in sync by hand
// with the identical struct in integration/cgroup_container_test.go,
// which lives in a different package and can't share this one directly.
type cgroupDump struct {
	MemMaxBytes  int64   `json:"mem_max_bytes"`
	HaveMemMax   bool    `json:"have_mem_max"`
	CoresLimit   float64 `json:"cores_limit"`
	CPUSource    string  `json:"cpu_source"`
	HaveCPULimit bool    `json:"have_cpu_limit"`
}

// dumpCgroupLimits mirrors the memory-limit read in collectFrame
// (render.go): try the cgroup v2 file, fall back to the v1 one.
func dumpCgroupLimits() {
	maxBytes, okMax := readCgroupVal("memory.max")
	if !okMax {
		maxBytes, okMax = readCgroupVal("memory.limit_in_bytes")
	}
	coresLimit, cpuSource, haveCPULimit := readCgroupCPULimit()

	json.NewEncoder(os.Stdout).Encode(cgroupDump{
		MemMaxBytes:  maxBytes,
		HaveMemMax:   okMax,
		CoresLimit:   coresLimit,
		CPUSource:    string(cpuSource),
		HaveCPULimit: haveCPULimit,
	})
}
