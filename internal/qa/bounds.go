package qa

// Viewport is one screen size that visual QA reads, in CSS pixels.
type Viewport struct {
	W int `json:"width"`
	H int `json:"height"`
}

// Bounds limit one QA run. Fuzzy UAT takes at most Steps toward the goal
// of each app, then at most Variations of at most VariationSteps each, in
// at most Minutes. Visual QA reads at most Screens of each app at each of
// Widths.
type Bounds struct {
	Steps          int        `json:"steps"`
	Variations     int        `json:"variations"`
	VariationSteps int        `json:"variation_steps"`
	Minutes        int        `json:"minutes"`
	Screens        int        `json:"screens"`
	Widths         []Viewport `json:"widths"`
}

// DefaultBounds is the one value of the bounds. Nothing else holds the
// numbers.
var DefaultBounds = Bounds{Steps: 25, Variations: 3, VariationSteps: 10, Minutes: 20, Screens: 8,
	Widths: []Viewport{{1280, 800}, {390, 844}}}
