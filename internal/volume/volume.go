// Package volume holds the app's volume rules.
//
// The stored level is the player *engine* gain (0–100, what you hear), read
// and written with #movie_player.getVolume/setVolume. YouTube Music has two
// player UIs in the wild: the newer mini player's slider is linear (slider N
// = engine N), while the older player bar and the PREF cookie (volume=N) use
// a steep curve where slider 1–4 means engine 0. Storing the engine level
// keeps loudness identical whichever UI YouTube serves.
package volume

import "math"

// Clamp rounds v to the nearest integer inside 0–100. ok is false for NaN/Inf.
func Clamp(v float64) (level int, ok bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return int(math.Max(0, math.Min(100, math.Round(v)))), true
}

// Measured on music.youtube.com (engine gain → old slider/PREF value).
var curve = [][2]float64{
	{0, 0}, {1, 5}, {2, 10}, {3, 15}, {5, 20}, {6, 25}, {8, 30}, {13, 40},
	{20, 50}, {29, 60}, {40, 70}, {55, 80}, {74, 90}, {100, 100},
}

func interpolate(v float64, from, to int) int {
	for i := 1; i < len(curve); i++ {
		x0, y0 := curve[i-1][from], curve[i-1][to]
		x1, y1 := curve[i][from], curve[i][to]
		if v <= x1 {
			return int(math.Round(y0 + (v-x0)/(x1-x0)*(y1-y0)))
		}
	}
	return 100
}

// EngineToSlider converts an engine level to the old slider / PREF scale.
// A non-zero engine level always maps to a non-zero slider value, so a
// PREF seeded from it never starts the player silent.
func EngineToSlider(engine float64) (level int, ok bool) {
	e, ok := Clamp(engine)
	if !ok {
		return 0, false
	}
	return interpolate(float64(e), 0, 1), true
}

// SliderToEngine converts an old slider / PREF value to the engine level.
// Values 1–4 are YouTube's silent range and map to the quietest audible
// engine level, 1, rather than to silence.
func SliderToEngine(slider float64) (level int, ok bool) {
	s, ok := Clamp(slider)
	if !ok {
		return 0, false
	}
	if s > 0 && s < 5 {
		return 1, true
	}
	return interpolate(float64(s), 1, 0), true
}
