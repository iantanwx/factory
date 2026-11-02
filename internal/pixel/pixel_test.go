package pixel

import (
	"image"
	"image/color"
	"math/big"
	"testing"
)

func fill(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func TestC23ChannelToleranceCountsAPixelPast25(t *testing.T) {
	base := color.NRGBA{R: 100, G: 100, B: 100, A: 200}
	for _, tc := range []struct {
		channel string
		shift   func(c *color.NRGBA, by uint8)
	}{
		{"red", func(c *color.NRGBA, by uint8) { c.R += by }},
		{"green", func(c *color.NRGBA, by uint8) { c.G += by }},
		{"blue", func(c *color.NRGBA, by uint8) { c.B += by }},
		{"alpha", func(c *color.NRGBA, by uint8) { c.A += by }},
	} {
		for _, by := range []uint8{25, 26} {
			other := base
			tc.shift(&other, by)
			got := Compare(fill(20, 10, base), fill(20, 10, other))
			want := Result{Total: 200}
			if by == 26 {
				want.Differing = 200
			}
			if got != want {
				t.Errorf("%s +%d: %+v, want %+v", tc.channel, by, got, want)
			}
			// The difference counts in either direction.
			if got := Compare(fill(20, 10, other), fill(20, 10, base)); got != want {
				t.Errorf("%s -%d: %+v, want %+v", tc.channel, by, got, want)
			}
		}
	}
}

func TestCompareCountsEachDifferingPixel(t *testing.T) {
	record := fill(200, 100, color.NRGBA{A: 255})
	capture := fill(200, 100, color.NRGBA{A: 255})
	for x := range 101 {
		capture.SetNRGBA(x, x%100, color.NRGBA{R: 255, A: 255})
	}
	if got, want := Compare(record, capture), (Result{Differing: 101, Total: 20000}); got != want {
		t.Fatalf("Compare = %+v, want %+v", got, want)
	}
}

func TestCompareFailsEveryPixelOfPicturesOfTwoSizes(t *testing.T) {
	c := color.NRGBA{A: 255}
	want := Result{Differing: 20000, Total: 20000, SizeMismatch: true}
	if got := Compare(fill(200, 100, c), fill(200, 101, c)); got != want {
		t.Fatalf("Compare = %+v, want %+v", got, want)
	}
}

func TestC22OverIsExactAtEachEdge(t *testing.T) {
	rat := func(s string) *big.Rat {
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			t.Fatalf("bad bound %q", s)
		}
		return r
	}
	for _, tc := range []struct {
		bound            string
		differing, total int
		over             bool
	}{
		{"0.5", 100, 20000, false},
		{"0.5", 101, 20000, true},
		{"0", 0, 20000, false},
		{"0", 1, 20000, true},
		{"5", 1000, 20000, false},
		{"5", 1001, 20000, true},
		{"0.125", 10, 8000, false},
		{"0.125", 11, 8000, true},
		{"0", 0, 0, false},
	} {
		r := Result{Differing: tc.differing, Total: tc.total}
		if got := Over(r, rat(tc.bound)); got != tc.over {
			t.Errorf("Over(%d/%d, %s%%) = %v, want %v", tc.differing, tc.total, tc.bound, got, tc.over)
		}
	}
}
