package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMeminfoInt(t *testing.T) {
	const meminfo = "MemTotal:       16384000 kB\nMemFree:         8192000 kB\n" +
		"MemAvailable:   12000000 kB\nSwapTotal:       2048000 kB\nSwapFree:        2048000 kB\n"

	tests := []struct {
		prefix  string
		wantVal int64
		wantOK  bool
	}{
		{"MemTotal:", 16384000, true},
		{"MemAvailable:", 12000000, true},
		{"SwapTotal:", 2048000, true},
		{"SwapFree:", 2048000, true},
		{"HugePages_Total:", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.prefix, func(t *testing.T) {
			gotVal, gotOK := meminfoInt(meminfo, tc.prefix)
			if gotOK != tc.wantOK || gotVal != tc.wantVal {
				t.Errorf("meminfoInt(_, %q) = (%d, %v), want (%d, %v)", tc.prefix, gotVal, gotOK, tc.wantVal, tc.wantOK)
			}
		})
	}
}

func TestParseCgroupName(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		wantName string
		wantOK   bool
	}{
		{
			name:     "cgroup v2 unified",
			data:     "0::/system.slice/docker-1a2b3c4d5e6f.scope\n",
			wantName: "system.slice/docker-1a2b3c4d5e6f.scope",
			wantOK:   true,
		},
		{
			name:     "cgroup v2 root (private cgroup namespace, e.g. containerized)",
			data:     "0::/\n",
			wantName: "",
			wantOK:   false,
		},
		{
			name: "cgroup v1 hybrid, memory controller preferred over others",
			data: "12:pids:/system.slice/system-vncserver.slice/vncserver@:1.service\n" +
				"11:cpuset:/\n" +
				"10:memory:/system.slice/system-vncserver.slice/vncserver@:1.service\n" +
				"1:name=systemd:/system.slice/system-vncserver.slice/vncserver@:1.service\n",
			wantName: "system.slice/system-vncserver.slice/vncserver@:1.service",
			wantOK:   true,
		},
		{
			name:     "cgroup v1, no memory line, falls back to any non-root controller",
			data:     "11:cpuset:/\n7:devices:/system.slice/foo.service\n",
			wantName: "system.slice/foo.service",
			wantOK:   true,
		},
		{
			name:     "everything at root: bare host, no confinement",
			data:     "12:pids:/\n11:cpuset:/\n10:memory:/\n1:name=systemd:/\n",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "empty input",
			data:     "",
			wantName: "",
			wantOK:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotOK := parseCgroupName(tc.data)
			if gotOK != tc.wantOK || gotName != tc.wantName {
				t.Errorf("parseCgroupName(%q) = (%q, %v), want (%q, %v)", tc.data, gotName, gotOK, tc.wantName, tc.wantOK)
			}
		})
	}
}

func TestHasCgroupMount(t *testing.T) {
	tests := []struct {
		name      string
		mountinfo string
		want      bool
	}{
		{
			name: "cgroup v1 hybrid: tmpfs root, cgroup-typed controllers nested below",
			mountinfo: "22 99 0:21 / /sys rw,nosuid shared:2 - sysfs sysfs rw\n" +
				"29 22 0:25 / /sys/fs/cgroup ro,nosuid,nodev,noexec shared:4 - tmpfs tmpfs ro,mode=755\n" +
				"30 29 0:26 / /sys/fs/cgroup/systemd rw,nosuid,nodev,noexec,relatime shared:5 - cgroup cgroup rw,xattr,name=systemd\n" +
				"37 29 0:33 / /sys/fs/cgroup/cpu,cpuacct rw,nosuid,nodev,noexec,relatime shared:9 - cgroup cgroup rw,cpu,cpuacct\n",
			want: true,
		},
		{
			name:      "cgroup v2 unified: single cgroup2 mount at the root",
			mountinfo: "29 22 0:25 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:4 - cgroup2 cgroup2 rw\n",
			want:      true,
		},
		{
			name: "no cgroup filesystem mounted anywhere: plain tmpfs, nothing nested below it",
			mountinfo: "22 99 0:21 / /sys rw,nosuid shared:2 - sysfs sysfs rw\n" +
				"29 22 0:25 / /sys/fs/cgroup ro,nosuid,nodev,noexec shared:4 - tmpfs tmpfs ro,mode=755\n",
			want: false,
		},
		{
			name:      "empty input",
			mountinfo: "",
			want:      false,
		},
		{
			name:      "malformed line without the \" - \" separator is skipped, not misread",
			mountinfo: "this line has no separator at all\n",
			want:      false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasCgroupMount(tc.mountinfo); got != tc.want {
				t.Errorf("hasCgroupMount(...) = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeCgroupOverride(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"bare name", "docker/1a2b3c4d5e6f", "docker/1a2b3c4d5e6f"},
		{"absolute v2 path", "/sys/fs/cgroup/docker/1a2b3c4d5e6f", "docker/1a2b3c4d5e6f"},
		{"trailing slash", "docker/1a2b3c4d5e6f/", "docker/1a2b3c4d5e6f"},
		{"surrounding whitespace", "  docker/1a2b3c4d5e6f  ", "docker/1a2b3c4d5e6f"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeCgroupOverride(tc.raw); got != tc.want {
				t.Errorf("normalizeCgroupOverride(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCgroupFile(t *testing.T) {
	old := cgroupSuffix
	defer func() { cgroupSuffix = old }()

	tests := []struct {
		name       string
		suffix     string
		controller string
		filename   string
		want       string
	}{
		{"v2, own cgroup", "", "", "memory.max", "/sys/fs/cgroup/memory.max"},
		{"v1, own cgroup", "", "memory", "memory.limit_in_bytes", "/sys/fs/cgroup/memory/memory.limit_in_bytes"},
		{"v2, overridden", "docker/1a2b3c4d5e6f", "", "memory.max", "/sys/fs/cgroup/docker/1a2b3c4d5e6f/memory.max"},
		{"v1, overridden", "docker/1a2b3c4d5e6f", "memory", "memory.limit_in_bytes", "/sys/fs/cgroup/memory/docker/1a2b3c4d5e6f/memory.limit_in_bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cgroupSuffix = tc.suffix
			if got := cgroupFile(tc.controller, tc.filename); got != tc.want {
				t.Errorf("cgroupFile(%q, %q) = %q, want %q", tc.controller, tc.filename, got, tc.want)
			}
		})
	}
}

func TestCgroupMemUnlimited(t *testing.T) {
	tests := []struct {
		name           string
		maxBytes       int64
		okMax          bool
		hostTotalBytes int64
		want           bool
	}{
		{"v2 max sentinel (parse failure)", 0, false, 16 << 30, true},
		{"v1 huge sentinel above host total", 1 << 62, true, 16 << 30, true},
		{"real limit under host total", 512 << 20, true, 16 << 30, false},
		{"limit equal to host total is still a real limit (strictly greater-than is unlimited)", 16 << 30, true, 16 << 30, false},
		{"zero or negative treated as unlimited", 0, true, 16 << 30, true},
		{"host total unknown: only okMax/maxBytes checked", 512 << 20, true, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cgroupMemUnlimited(tc.maxBytes, tc.okMax, tc.hostTotalBytes); got != tc.want {
				t.Errorf("cgroupMemUnlimited(%d, %v, %d) = %v, want %v", tc.maxBytes, tc.okMax, tc.hostTotalBytes, got, tc.want)
			}
		})
	}
}

func TestFormatCgroupStatusFields(t *testing.T) {
	tests := []struct {
		name         string
		st           cgroupStatus
		wantMemCurr  string
		wantMemLimit string
		wantCPULimit string
	}{
		{
			name:         "no data at all",
			st:           cgroupStatus{},
			wantMemCurr:  "-",
			wantMemLimit: "-",
			wantCPULimit: "-",
		},
		{
			name: "memory bounded, quota-limited CPU",
			st: cgroupStatus{
				haveMemCurrent: true, memCurrentBytes: 512 << 20,
				memMaxBytes: 2 << 30,
				haveCPU:     true, cpuCores: 2, cpuSource: cpuSourceQuota,
			},
			wantMemCurr:  "512M",
			wantMemLimit: "2.0G",
			wantCPULimit: "2.00 cores (quota)",
		},
		{
			name: "memory readable but unlimited, no CPU limit",
			st: cgroupStatus{
				haveMemCurrent: true, memCurrentBytes: 48 << 20, memUnlimited: true,
			},
			wantMemCurr:  "48M",
			wantMemLimit: "(no limit)",
			wantCPULimit: "-",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatMemCurrent(tc.st); got != tc.wantMemCurr {
				t.Errorf("formatMemCurrent() = %q, want %q", got, tc.wantMemCurr)
			}
			if got := formatMemLimit(tc.st); got != tc.wantMemLimit {
				t.Errorf("formatMemLimit() = %q, want %q", got, tc.wantMemLimit)
			}
			if got := formatCPULimit(tc.st); got != tc.wantCPULimit {
				t.Errorf("formatCPULimit() = %q, want %q", got, tc.wantCPULimit)
			}
		})
	}
}

func TestPrintCgroupList(t *testing.T) {
	t.Run("not mounted", func(t *testing.T) {
		var buf bytes.Buffer
		printCgroupList(&buf, cgroupHostInfo{mounted: false}, nil)
		if !strings.Contains(buf.String(), "not mounted") {
			t.Errorf("output = %q, want it to mention \"not mounted\"", buf.String())
		}
	})

	t.Run("mounted, no cgroups found", func(t *testing.T) {
		var buf bytes.Buffer
		printCgroupList(&buf, cgroupHostInfo{mounted: true, unified: true, controllers: []string{"cpu", "memory"}}, nil)
		out := buf.String()
		if !strings.Contains(out, "cgroup v2 (unified)") {
			t.Errorf("output = %q, want it to mention the v2 version", out)
		}
		if !strings.Contains(out, "controllers: cpu memory") {
			t.Errorf("output = %q, want it to list controllers", out)
		}
		if !strings.Contains(out, "no cgroups found") {
			t.Errorf("output = %q, want it to say no cgroups were found", out)
		}
	})

	t.Run("mounted, one cgroup", func(t *testing.T) {
		var buf bytes.Buffer
		host := cgroupHostInfo{mounted: true, unified: false}
		statuses := []cgroupStatus{
			{name: "docker/1a2b3c4d5e6f", haveMemCurrent: true, memCurrentBytes: 512 << 20, memMaxBytes: 2 << 30,
				haveCPU: true, cpuCores: 2, cpuSource: cpuSourceQuota},
		}
		printCgroupList(&buf, host, statuses)
		out := buf.String()
		if !strings.Contains(out, "v1 (legacy/hybrid)") {
			t.Errorf("output = %q, want it to mention the v1 version", out)
		}
		if !strings.Contains(out, "docker/1a2b3c4d5e6f") {
			t.Errorf("output = %q, want it to list the cgroup name", out)
		}
		if !strings.Contains(out, "512M") || !strings.Contains(out, "2.0G") || !strings.Contains(out, "2.00 cores (quota)") {
			t.Errorf("output = %q, want it to include the status columns", out)
		}
	})
}

func TestFirstLineWithPrefix(t *testing.T) {
	const meminfo = "MemTotal:       16384000 kB\nMemFree:         8192000 kB\nMemAvailable:   12000000 kB\n"

	tests := []struct {
		name     string
		s        string
		prefix   string
		wantLine string
		wantOK   bool
	}{
		{"first line", meminfo, "MemTotal:", "MemTotal:       16384000 kB", true},
		{"middle line", meminfo, "MemFree:", "MemFree:         8192000 kB", true},
		{"last line", meminfo, "MemAvailable:", "MemAvailable:   12000000 kB", true},
		{"no match", meminfo, "SwapTotal:", "", false},
		{"no trailing newline", "usage_usec 42", "usage_usec", "usage_usec 42", true},
		{"empty input", "", "MemTotal:", "", false},
		{"prefix is not a whole-line anchor", "xMemTotal: 1\n", "MemTotal:", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotLine, gotOK := firstLineWithPrefix(tc.s, tc.prefix)
			if gotOK != tc.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotLine != tc.wantLine {
				t.Errorf("line = %q, want %q", gotLine, tc.wantLine)
			}
		})
	}
}
