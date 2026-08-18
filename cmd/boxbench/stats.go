package main

import (
	"math"
	"sort"
)

// computeStats summarizes a set of repeated samples of one metric (e.g.
// wall-clock milliseconds across --runs process invocations). samples must
// be non-empty.
func computeStats(samples []float64) MetricStats {
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)

	m := mean(sorted)
	return MetricStats{
		Mean:   m,
		Median: median(sorted),
		StdDev: stddev(sorted, m),
		Min:    sorted[0],
		Max:    sorted[len(sorted)-1],
	}
}

func mean(samples []float64) float64 {
	var sum float64
	for _, v := range samples {
		sum += v
	}
	return sum / float64(len(samples))
}

// median assumes samples is already sorted.
func median(samples []float64) float64 {
	n := len(samples)
	if n%2 == 1 {
		return samples[n/2]
	}
	return (samples[n/2-1] + samples[n/2]) / 2
}

// stddev is the sample standard deviation (n-1 denominator); a single
// sample has no spread to measure, so it reports 0 rather than dividing by
// zero.
func stddev(samples []float64, m float64) float64 {
	if len(samples) < 2 {
		return 0
	}
	var sumSq float64
	for _, v := range samples {
		d := v - m
		sumSq += d * d
	}
	return math.Sqrt(sumSq / float64(len(samples)-1))
}
