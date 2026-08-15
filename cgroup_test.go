package main

import "testing"

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
