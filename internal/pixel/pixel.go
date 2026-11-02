// Package pixel compares the capture of one state with the record's
// picture of it, for the pixel gate (factorio-4lso). It does no I/O.
package pixel

import (
	"image"
	"image/color"
	"math/big"
)

// ChannelTolerance is the most that one 8-bit channel of R, G, B and A may
// differ with the pixel still the same. A larger difference in any one
// channel counts the pixel.
const ChannelTolerance = 25

// Result is the comparison of two pictures.
type Result struct {
	Differing    int  // the pixels that differ past ChannelTolerance
	Total        int  // the pixel count of the record's picture
	SizeMismatch bool // the pictures differ in width or height
}

// Compare compares the capture with the record's picture, pixel by pixel.
// Pictures of two sizes differ in every pixel: a crop or a scale would
// hide a change of layout.
func Compare(record, capture image.Image) Result {
	rb, cb := record.Bounds(), capture.Bounds()
	res := Result{Total: rb.Dx() * rb.Dy()}
	if rb.Size() != cb.Size() {
		res.Differing, res.SizeMismatch = res.Total, true
		return res
	}
	for y := range rb.Dy() {
		for x := range rb.Dx() {
			a := color.NRGBAModel.Convert(record.At(rb.Min.X+x, rb.Min.Y+y)).(color.NRGBA)
			b := color.NRGBAModel.Convert(capture.At(cb.Min.X+x, cb.Min.Y+y)).(color.NRGBA)
			if past(a.R, b.R) || past(a.G, b.G) || past(a.B, b.B) || past(a.A, b.A) {
				res.Differing++
			}
		}
	}
	return res
}

func past(a, b uint8) bool { return max(a, b)-min(a, b) > ChannelTolerance }

// Over reports whether the share of differing pixels is over bound, a
// percent: Differing/Total > bound/100, exactly.
func Over(r Result, bound *big.Rat) bool {
	if r.Total == 0 {
		return false
	}
	share := big.NewRat(int64(r.Differing)*100, int64(r.Total))
	return share.Cmp(bound) > 0
}
