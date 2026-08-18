//go:build integration

// Package integration holds boxtop's container-based integration tests.
// This one is excluded from the default `go test ./...` run (see
// CONTRIBUTING.md) because it shells out to a container runtime and
// actually creates a container, which is slow and requires Docker. Run it
// explicitly with:
//
//	go test -tags integration -run TestCgroupDetectionInContainer -v ./integration
//
// It works by compiling boxtop's own test binary (via `go test -c` against
// the module root), then running that binary *inside* a container with
// known --memory/--cpus limits. When invoked with BOXTOP_CGROUP_DUMP=1 the
// test binary skips the normal `go test` machinery (see TestMain in
// ../cgroup_dump_test.go) and instead prints what boxtop's own
// cgroup-reading code — the same unexported readCgroupVal/
// readCgroupCPULimit functions collectFrame in render.go calls on every
// tick — detected from inside that container, as JSON on stdout. This test
// then compares that against the limits it asked Docker for.
package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// cgroupDump mirrors the identical struct in ../cgroup_dump_test.go, which
// lives in package main and can't be imported directly from a _test.go
// file (nor would we want to: the worker binary and this driver are
// deliberately two separate processes, only coupled by this JSON shape).
type cgroupDump struct {
	MemMaxBytes  int64   `json:"mem_max_bytes"`
	HaveMemMax   bool    `json:"have_mem_max"`
	CoresLimit   float64 `json:"cores_limit"`
	CPUSource    string  `json:"cpu_source"`
	HaveCPULimit bool    `json:"have_cpu_limit"`
	SwapMaxBytes int64   `json:"swap_max_bytes"`
	HaveSwapMax  bool    `json:"have_swap_max"`
}

// wantCPUSource mirrors cgroup.go's unexported cpuSourceQuota constant —
// a --cpus quota should be detected as a quota, not a cpuset or host
// fallback.
const wantCPUSource = "quota"

func TestCgroupDetectionInContainer(t *testing.T) {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker not found in PATH; skipping container integration test")
	}
	if out, err := exec.Command(dockerPath, "info").CombinedOutput(); err != nil {
		t.Skipf("docker daemon not reachable, skipping: %v\n%s", err, out)
	}
	// Rootless Docker without host support for delegated cgroup v2
	// controllers reports "none" here and silently ignores --memory/
	// --cpus instead of enforcing them, which would make every
	// assertion below fail for reasons that have nothing to do with
	// boxtop's own cgroup-reading code.
	if out, err := exec.Command(dockerPath, "info", "--format", "{{.CgroupDriver}}").Output(); err == nil {
		if driver := strings.TrimSpace(string(out)); driver == "none" {
			t.Skipf("docker reports Cgroup Driver: none (no delegated cgroup support, common for rootless Docker on a cgroup v1 host); skipping since --memory/--cpus won't be enforced")
		}
	}

	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}

	binPath := filepath.Join(t.TempDir(), "boxtop-cgroup-dump")
	build := exec.Command("go", "test", "-c", "-tags", "integration", "-o", binPath, ".")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building worker binary: %v\n%s", err, out)
	}

	const wantMemBytes = int64(256 * 1024 * 1024)  // 256MiB, page-aligned so the kernel reports it back exactly
	const wantSwapBytes = int64(128 * 1024 * 1024) // on top of wantMemBytes, i.e. --memory-swap = mem+swap
	const wantCores = 1.5

	run := exec.Command(dockerPath, "run", "--rm",
		"--memory", strconv.FormatInt(wantMemBytes, 10),
		"--memory-swap", strconv.FormatInt(wantMemBytes+wantSwapBytes, 10),
		"--cpus", strconv.FormatFloat(wantCores, 'f', -1, 64),
		"-e", "BOXTOP_CGROUP_DUMP=1",
		"-v", binPath+":/boxtop-cgroup-dump:ro",
		// alpine, not scratch: "scratch" is a Dockerfile-only pseudo-base
		// with no manifest, so `docker run scratch` fails with "reserved
		// name" — it can't be pulled/run directly. alpine is real but
		// still tiny; the worker itself needs no libc either way, since
		// it's a static CGO_ENABLED=0 binary run directly as PID 1.
		"alpine",
		"/boxtop-cgroup-dump",
	)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run failed: %v\n%s", err, out)
	}

	var got cgroupDump
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("parsing worker output: %v\nraw output: %s", err, out)
	}

	if !got.HaveMemMax {
		t.Error("memory limit not detected from inside the container")
	} else if got.MemMaxBytes != wantMemBytes {
		t.Errorf("mem_max_bytes = %d, want %d", got.MemMaxBytes, wantMemBytes)
	}

	if !got.HaveCPULimit {
		t.Error("CPU limit not detected from inside the container")
		return
	}
	if got.CoresLimit != wantCores {
		t.Errorf("cores_limit = %v, want %v", got.CoresLimit, wantCores)
	}
	if got.CPUSource != wantCPUSource {
		t.Errorf("cpu_source = %q, want %q", got.CPUSource, wantCPUSource)
	}

	// Swap accounting can be compiled out of the kernel entirely (e.g.
	// swapaccount=0 on the boot cmdline, common on some distros), in which
	// case memory.swap.max simply doesn't exist — that's a host
	// configuration fact, not a bug in boxtop's reader, so it's a skip
	// rather than a failure.
	if !got.HaveSwapMax {
		t.Log("swap limit not detected — host kernel likely has swap accounting disabled (swapaccount=0); skipping swap assertion")
		return
	}
	if got.SwapMaxBytes != wantSwapBytes {
		t.Errorf("swap_max_bytes = %d, want %d", got.SwapMaxBytes, wantSwapBytes)
	}
}
