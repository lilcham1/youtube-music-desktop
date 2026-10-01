package volume

import (
	"math"
	"testing"
)

func TestClamp(t *testing.T) {
	for in, want := range map[float64]int{-10: 0, 0: 0, 2.6: 3, 42: 42, 120: 100} {
		if got, ok := Clamp(in); !ok || got != want {
			t.Errorf("Clamp(%v) = %d,%v; want %d", in, got, ok, want)
		}
	}
	if _, ok := Clamp(math.NaN()); ok {
		t.Error("NaN must be rejected")
	}
}

func TestCurveMatchesMeasurements(t *testing.T) {
	for engine, slider := range map[float64]int{0: 0, 1: 5, 5: 20, 8: 30, 20: 50, 30: 61, 55: 80, 100: 100} {
		if got, _ := EngineToSlider(engine); got != slider {
			t.Errorf("EngineToSlider(%v) = %d; want %d", engine, got, slider)
		}
	}
	for slider, engine := range map[float64]int{0: 0, 1: 1, 4: 1, 5: 1, 10: 2, 50: 20, 80: 55, 100: 100} {
		if got, _ := SliderToEngine(slider); got != engine {
			t.Errorf("SliderToEngine(%v) = %d; want %d", slider, got, engine)
		}
	}
}

// A seeded PREF must never start the player silent, and converting there
// and back must stay close to the requested loudness.
func TestRoundTripIsAudibleAndClose(t *testing.T) {
	prevSlider := -1
	for engine := 0; engine <= 100; engine++ {
		slider, _ := EngineToSlider(float64(engine))
		if engine > 0 && slider < 5 {
			t.Fatalf("engine %d seeds silent slider %d", engine, slider)
		}
		if slider < prevSlider {
			t.Fatalf("EngineToSlider not monotonic at %d", engine)
		}
		prevSlider = slider
		back, _ := SliderToEngine(float64(slider))
		if math.Abs(float64(back-engine)) > 1 {
			t.Fatalf("engine %d → slider %d → engine %d", engine, slider, back)
		}
	}
}
