package scoring

import (
	"strings"
)

// EstimateResale returns a conservative post-repair private-party resale value
// for the OC market based on make/model/year/mileage.
func EstimateResale(year int, make, model string, odo int) int {
	base := baseResale(strings.ToUpper(make), strings.ToUpper(model))
	if base == 0 {
		return 0
	}

	// 12% depreciation per year of age (capped at 6 years)
	age := 2026 - year
	if age < 0 {
		age = 0
	}
	if age > 6 {
		age = 6
	}
	resale := int(float64(base) * (1.0 - float64(age)*0.12))

	// -$400 per 10K miles over 20K
	if odo > 20_000 {
		resale -= ((odo - 20_000) / 10_000) * 400
	}

	if resale < 8_000 {
		resale = 8_000
	}
	return resale
}

// MaxBid returns the maximum auction bid to achieve targetROI (e.g. 0.50 for 50%)
// given the estimated resale and mid-point repair cost.
// Formula: bid = resale / (1 + targetROI) - repairMid
func MaxBid(resale, repairMid int, targetROI float64) int {
	if resale == 0 || targetROI <= 0 {
		return 0
	}
	mb := int(float64(resale)/(1+targetROI)) - repairMid
	if mb < 0 {
		return 0
	}
	return mb
}

// ROIPercent returns the ROI as a whole-number percentage.
// ROI = (resale - totalCost) / totalCost * 100
func ROIPercent(bid, repairMid, resale int) int {
	total := bid + repairMid
	if total <= 0 || resale <= 0 {
		return 0
	}
	return int(float64(resale-total) / float64(total) * 100)
}

// baseResale is a conservative OC private-party post-repair price anchored to 2026.
var baseResale = func(make, model string) int {
	type entry struct {
		prefix string
		value  int
	}
	models := map[string][]entry{
		"TOYOTA": {
			{"TACOMA", 38_000},
			{"4RUNNER", 40_000},
			{"HIGHLANDER HYBRID", 42_000},
			{"HIGHLANDER", 38_000},
			{"RAV4 PRIME", 38_000},
			{"RAV4 HYBRID", 34_000},
			{"RAV4", 30_000},
			{"CAMRY HYBRID", 28_000},
			{"CAMRY", 25_000},
			{"COROLLA CROSS HYBRID", 26_000},
			{"COROLLA CROSS", 24_000},
			{"COROLLA HYBRID", 22_000},
			{"COROLLA", 20_000},
			{"VENZA", 32_000},
			{"SIENNA", 36_000},
		},
		"HONDA": {
			{"PILOT", 36_000},
			{"PASSPORT", 32_000},
			{"ODYSSEY", 30_000},
			{"CR-V HYBRID", 32_000},
			{"CR-V", 28_000},
			{"CRV", 28_000},
			{"ACCORD HYBRID", 28_000},
			{"ACCORD", 24_000},
			{"CIVIC TYPE R", 44_000},
			{"CIVIC", 22_000},
			{"HR-V", 22_000},
			{"HRV", 22_000},
		},
		"LEXUS": {
			{"RX 500H", 58_000},
			{"RX 450H", 52_000},
			{"RX", 46_000},
			{"NX 450H", 48_000},
			{"NX", 42_000},
			{"ES 300H", 42_000},
			{"ES", 38_000},
			{"UX", 32_000},
			{"GX", 52_000},
			{"LX", 80_000},
		},
	}

	entries, ok := models[make]
	if !ok {
		return 0
	}
	for _, e := range entries {
		if strings.HasPrefix(model, e.prefix) {
			return e.value
		}
	}
	// Fallback by make
	switch make {
	case "TOYOTA":
		return 22_000
	case "HONDA":
		return 22_000
	case "LEXUS":
		return 38_000
	}
	return 0
}
