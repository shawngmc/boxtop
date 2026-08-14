package main

import "testing"

const sampleCPUInfo = "processor\t: 0\n" +
	"vendor_id\t: GenuineIntel\n" +
	"model name\t: Intel(R) Core(TM) i7-9700K CPU @ 3.60GHz\n" +
	"cpu MHz\t\t: 4200.123\n" +
	"processor\t: 1\n" +
	"model name\t: Intel(R) Core(TM) i7-9700K CPU @ 3.60GHz\n" +
	"cpu MHz\t\t: 3600.000\n"

func TestParseCPUModel(t *testing.T) {
	tests := []struct {
		name      string
		cpuinfo   string
		wantModel string
		wantOK    bool
	}{
		{"normal", sampleCPUInfo, "Intel(R) Core(TM) i7-9700K CPU @ 3.60GHz", true},
		{"no match", "processor\t: 0\nvendor_id\t: GenuineIntel\n", "", false},
		{"empty value", "model name\t:   \n", "", false},
		{"empty input", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotModel, gotOK := parseCPUModel(tc.cpuinfo)
			if gotOK != tc.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotModel != tc.wantModel {
				t.Errorf("model = %q, want %q", gotModel, tc.wantModel)
			}
		})
	}
}

func TestParseCPUInfoMHz(t *testing.T) {
	tests := []struct {
		name    string
		cpuinfo string
		want    float64
		wantOK  bool
	}{
		{"normal", sampleCPUInfo, 4200.123, true},
		{"no match", "processor\t: 0\n", 0, false},
		{"non-numeric", "cpu MHz\t\t: fast\n", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotOK := parseCPUInfoMHz(tc.cpuinfo)
			if gotOK != tc.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotOK && got != tc.want {
				t.Errorf("mhz = %v, want %v", got, tc.want)
			}
		})
	}
}
