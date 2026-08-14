package main

import "testing"

// TestUseColorblindPalette checks that the --colorblind switch actually
// swaps the package-level gradients (not a no-op), and that it can be
// flipped back to the default palette.
func TestUseColorblindPalette(t *testing.T) {
	defer useColorblindPalette(false) // don't leak state into other tests

	useColorblindPalette(false)
	defaultLow := summaryStops[0]
	if defaultLow.r != green[0] || defaultLow.g != green[1] || defaultLow.b != green[2] {
		t.Fatalf("default palette low stop = %+v, want green", defaultLow)
	}

	useColorblindPalette(true)
	cbLow := summaryStops[0]
	if cbLow.r != cbBlue[0] || cbLow.g != cbBlue[1] || cbLow.b != cbBlue[2] {
		t.Errorf("colorblind palette low stop = %+v, want blue", cbLow)
	}
	if processCPUStops[0] != processCPUStopsColorblind[0] {
		t.Errorf("processCPUStops not swapped to the colorblind set")
	}

	useColorblindPalette(false)
	backLow := summaryStops[0]
	if backLow.r != green[0] || backLow.g != green[1] || backLow.b != green[2] {
		t.Errorf("useColorblindPalette(false) did not restore default palette, got %+v", backLow)
	}
}

// TestInterpolateStopsColorblindPalette sanity-checks that the colorblind
// stops still interpolate as a valid gradient: monotonic from the low
// stop's color to the high stop's color, clamped outside [0,1].
func TestInterpolateStopsColorblindPalette(t *testing.T) {
	r0, g0, b0 := interpolateStops(0, summaryStopsColorblind)
	if r0 != cbBlue[0] || g0 != cbBlue[1] || b0 != cbBlue[2] {
		t.Errorf("interpolateStops(0) = (%d,%d,%d), want blue", r0, g0, b0)
	}
	r1, g1, b1 := interpolateStops(1, summaryStopsColorblind)
	if r1 != cbVermilion[0] || g1 != cbVermilion[1] || b1 != cbVermilion[2] {
		t.Errorf("interpolateStops(1) = (%d,%d,%d), want vermilion", r1, g1, b1)
	}
	// Out-of-range fracs clamp to the endpoint stops, same as the default
	// palette's documented behavior.
	rNeg, gNeg, bNeg := interpolateStops(-1, summaryStopsColorblind)
	if rNeg != r0 || gNeg != g0 || bNeg != b0 {
		t.Errorf("interpolateStops(-1) did not clamp to the low stop")
	}
}
