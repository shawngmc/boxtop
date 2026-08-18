package main

import (
	"math"
	"testing"
)

func floatsClose(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestComputeStats(t *testing.T) {
	got := computeStats([]float64{1, 2, 3, 4, 5})

	if !floatsClose(got.Mean, 3) {
		t.Errorf("Mean = %v, want 3", got.Mean)
	}
	if !floatsClose(got.Median, 3) {
		t.Errorf("Median = %v, want 3", got.Median)
	}
	if !floatsClose(got.Min, 1) {
		t.Errorf("Min = %v, want 1", got.Min)
	}
	if !floatsClose(got.Max, 5) {
		t.Errorf("Max = %v, want 5", got.Max)
	}
	// Sample stddev of {1,2,3,4,5} is sqrt(2.5) ~= 1.5811388.
	if !floatsClose(got.StdDev, math.Sqrt(2.5)) {
		t.Errorf("StdDev = %v, want %v", got.StdDev, math.Sqrt(2.5))
	}
}

func TestComputeStatsSingleSample(t *testing.T) {
	got := computeStats([]float64{42})
	if got.Mean != 42 || got.Median != 42 || got.Min != 42 || got.Max != 42 {
		t.Errorf("single-sample stats = %+v, want all fields 42", got)
	}
	if got.StdDev != 0 {
		t.Errorf("StdDev = %v, want 0 for a single sample", got.StdDev)
	}
}

func TestComputeStatsDoesNotMutateInput(t *testing.T) {
	samples := []float64{5, 1, 3}
	_ = computeStats(samples)
	if samples[0] != 5 || samples[1] != 1 || samples[2] != 3 {
		t.Errorf("computeStats mutated its input: %v", samples)
	}
}

func TestMedianEvenCount(t *testing.T) {
	// median() assumes sorted input.
	got := median([]float64{1, 2, 3, 4})
	if !floatsClose(got, 2.5) {
		t.Errorf("median([1,2,3,4]) = %v, want 2.5", got)
	}
}
