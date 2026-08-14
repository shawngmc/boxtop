package main

import "github.com/gdamore/tcell/v2"

// colorStop mirrors a (position, (r, g, b)) entry in the Python STOPS
// lists — position in [0, 1] mapped to an RGB triple.
type colorStop struct {
	pos     float64
	r, g, b int32
}

var (
	green = [3]int32{0, 255, 0}
	// yellow = [3]int32{255, 255, 0} (inlined below for readability)
	red = [3]int32{255, 0, 0}
)

// summaryStops ports SUMMARY_STOPS: plain 0%->100%-of-limit gradient used
// for the RAM and CPU summary bars.
var summaryStops = []colorStop{
	{0.0, green[0], green[1], green[2]},
	{0.5, 255, 255, 0},
	{1.0, red[0], red[1], red[2]},
}

// processStops ports PROCESS_STOPS: a much steeper curve for the
// per-process RSS column — a single process eating a large share of the
// whole cgroup's budget is alarming well before 50%.
var processStops = []colorStop{
	{0.0, green[0], green[1], green[2]},
	{0.10, 255, 255, 0},
	{0.50, red[0], red[1], red[2]},
}

// processCPUStops ports PROCESS_CPU_STOPS: reuses the gentler summary
// curve, since a process maxing its cgroup's CPU budget is normal (unlike
// memory, where one process eating a large share raises OOM-kill risk for
// the whole cgroup).
var processCPUStops = summaryStops

// Colorblind-safe replacements for the stops above. Red and green are the
// pair confused by the two most common forms of color blindness
// (deuteranopia/protanopia), so the default green-yellow-red gradient can
// collapse to a single muddy hue for those users right where the gradient
// is meant to be most informative. Swapping green for blue keeps the same
// three-stop shape (and the same yellow midpoint) while using two endpoint
// colors — blue and vermillion — that stay distinguishable under all
// common forms of color blindness. Values are from the Okabe-Ito palette
// (https://jfly.uni-koeln.de/color/), which was designed and vetted for
// exactly this.
var (
	cbBlue      = [3]int32{0, 114, 178}
	cbYellow    = [3]int32{240, 228, 66}
	cbVermilion = [3]int32{213, 94, 0}
)

var summaryStopsColorblind = []colorStop{
	{0.0, cbBlue[0], cbBlue[1], cbBlue[2]},
	{0.5, cbYellow[0], cbYellow[1], cbYellow[2]},
	{1.0, cbVermilion[0], cbVermilion[1], cbVermilion[2]},
}

var processStopsColorblind = []colorStop{
	{0.0, cbBlue[0], cbBlue[1], cbBlue[2]},
	{0.10, cbYellow[0], cbYellow[1], cbYellow[2]},
	{0.50, cbVermilion[0], cbVermilion[1], cbVermilion[2]},
}

var processCPUStopsColorblind = summaryStopsColorblind

// useColorblindPalette swaps the package's active gradients to the
// colorblind-safe set (or back to the default set), for the --colorblind
// flag in main.go. Called once at startup, before the first draw.
func useColorblindPalette(on bool) {
	if on {
		summaryStops = summaryStopsColorblind
		processStops = processStopsColorblind
		processCPUStops = processCPUStopsColorblind
	} else {
		summaryStops = []colorStop{
			{0.0, green[0], green[1], green[2]},
			{0.5, 255, 255, 0},
			{1.0, red[0], red[1], red[2]},
		}
		processStops = []colorStop{
			{0.0, green[0], green[1], green[2]},
			{0.10, 255, 255, 0},
			{0.50, red[0], red[1], red[2]},
		}
		processCPUStops = summaryStops
	}
}

// interpolateStops ports gradient_color(): maps frac in [0, 1] to an RGB
// triple by linearly interpolating through stops (sorted ascending by
// position). frac is clamped to the stops' own range, same as the Python
// version — a value past the last stop renders as that stop's color
// rather than extrapolating further.
func interpolateStops(frac float64, stops []colorStop) (int32, int32, int32) {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}

	if frac <= stops[0].pos {
		return stops[0].r, stops[0].g, stops[0].b
	}
	last := stops[len(stops)-1]
	if frac >= last.pos {
		return last.r, last.g, last.b
	}

	for i := 0; i < len(stops)-1; i++ {
		p0, p1 := stops[i], stops[i+1]
		if frac >= p0.pos && frac <= p1.pos {
			t := 0.0
			if p1.pos > p0.pos {
				t = (frac - p0.pos) / (p1.pos - p0.pos)
			}
			r := p0.r + int32(float64(p1.r-p0.r)*t)
			g := p0.g + int32(float64(p1.g-p0.g)*t)
			b := p0.b + int32(float64(p1.b-p0.b)*t)
			return r, g, b
		}
	}

	// Unreachable given the clamps above, but keep a safe fallback.
	return last.r, last.g, last.b
}

// gradientStyle is the tcell equivalent of colorize(): builds a
// foreground-colored Style for the given fraction along stops, in place
// of the Python version's "\033[38;2;r;g;bm...RESET" string wrapping.
func gradientStyle(frac float64, stops []colorStop) tcell.Style {
	r, g, b := interpolateStops(frac, stops)
	return tcell.StyleDefault.Foreground(tcell.NewRGBColor(r, g, b))
}
