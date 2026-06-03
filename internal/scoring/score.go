package scoring

import (
	"strings"

	"github.com/vincentrosso/ferret/scrapers/copart"
)

// Score holds the breakdown and total for a single lot.
// When Damage is 0 and DamageKnown is false, damage is unanalyzed (neutral 15 applied).
type Score struct {
	Total       int  `json:"total"`
	Damage      int  `json:"damage"`       // 0–30: light=30, moderate=10, heavy=3, severe=0
	DamageKnown bool `json:"damage_known"` // false when not yet analyzed
	Make        int  `json:"make"`         // 0–20: sell-speed by make
	Mileage     int  `json:"mileage"`      // 0–20: lower miles = higher score
	Year        int  `json:"year"`         // 0–15: newer = higher score
	Bid         int  `json:"bid"`          // 0–10: lower current bid = higher ROI potential
	ModelTier   int  `json:"model_tier"`   // 0–5: top-demand models
}

// RankedLot is a lot with its score attached, ready for JSON output.
type RankedLot struct {
	copart.Lot
	Score Score `json:"score"`
}

// Rank scores a lot without damage info (damage treated as neutral).
func Rank(lot copart.Lot) Score {
	return RankWithDamage(lot, "")
}

// RankWithDamage scores a lot including the damage severity from a vision analysis.
// severity: "light", "moderate", "heavy", "severe", or "" (unknown → neutral).
func RankWithDamage(lot copart.Lot, severity string) Score {
	dmg, known := damageSeverityScore(severity)
	s := Score{
		Damage:      dmg,
		DamageKnown: known,
		Make:        makeLiquidity(lot.Make),
		Mileage:     mileageScore(lot.Odometer),
		Year:        yearScore(lot.Year),
		Bid:         bidScore(lot.CurrentBid),
		ModelTier:   modelTierScore(lot.Make, lot.Model),
	}
	s.Total = s.Damage + s.Make + s.Mileage + s.Year + s.Bid + s.ModelTier
	return s
}

// DamageSeverityScore returns the score (0–30) and whether severity was known.
// light=30, moderate=10, heavy=3, severe=0, unknown=15 (neutral).
func damageSeverityScore(severity string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "light":
		return 30, true
	case "moderate":
		return 10, true
	case "heavy":
		return 3, true
	case "severe":
		return 0, true
	default:
		return 15, false // neutral placeholder
	}
}

// RankAll scores every lot and returns them sorted best-first.
func RankAll(lots []copart.Lot) []RankedLot {
	out := make([]RankedLot, len(lots))
	for i, l := range lots {
		out[i] = RankedLot{Lot: l, Score: Rank(l)}
	}
	// sort descending by total score
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Score.Total > out[j-1].Score.Total; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Top scores and returns only lots at or above the Nth percentile, sorted best-first.
// Pass 80 to get the top 20%, 90 for top 10%.
func Top(lots []copart.Lot, percentile int) []RankedLot {
	all := make([]scoredLot, len(lots))
	for i, l := range lots {
		all[i] = scoredLot{l, Rank(l).Total}
	}
	if percentile <= 0 {
		percentile = 80
	}
	cutoff := percentileScore(all, percentile)

	var out []RankedLot
	for _, s := range all {
		if s.total >= cutoff {
			out = append(out, RankedLot{Lot: s.lot, Score: Rank(s.lot)})
		}
	}
	// sort descending
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Score.Total > out[j-1].Score.Total; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ── factor scorers ────────────────────────────────────────────────────────────

func makeLiquidity(make string) int {
	switch strings.ToUpper(make) {
	case "TOYOTA":
		return 20
	case "HONDA":
		return 16
	case "LEXUS":
		return 12
	}
	return 0
}

func mileageScore(odo int) int {
	switch {
	case odo < 25_000:
		return 20
	case odo < 50_000:
		return 16
	case odo < 70_000:
		return 10
	case odo < 85_000:
		return 4
	default:
		return 0
	}
}

// maxAffordableYear is a budget gate: the user is buying ≤~$10k, and clean-title
// hail lots from newer years reliably hammer well above that. Year is the only
// price proxy available at scrape-scoring time (expected hammer price isn't
// computed until the later valuation step). Bump this when the budget grows.
const maxAffordableYear = 2023

func yearScore(year int) int {
	// Out of budget → sink below every eligible lot (stays visible, never ranks
	// in the top picks). Penalty exceeds the max possible positive total (~85).
	if year > maxAffordableYear {
		return -1000
	}
	// Within the affordable pool, favor the recent-but-cheap sweet spot
	// (2020–2023) and taper for older, higher-repair, slower-resale cars.
	switch {
	case year >= 2022:
		return 15
	case year == 2021:
		return 13
	case year == 2020:
		return 11
	case year >= 2018:
		return 8
	case year >= 2015:
		return 5
	case year >= 2012:
		return 3
	default:
		return 1
	}
}

func bidScore(bid float64) int {
	switch {
	case bid <= 0:
		return 10
	case bid < 2_000:
		return 9
	case bid < 4_000:
		return 7
	case bid < 6_000:
		return 5
	case bid < 8_000:
		return 2
	default:
		return 0
	}
}

// topModels lists models with strong OC resale + fast turnover.
var topModels = map[string][]string{
	"TOYOTA": {"RAV4", "CAMRY", "TACOMA", "4RUNNER", "HIGHLANDER", "COROLLA CROSS"},
	"HONDA":  {"CR-V", "CRV", "ACCORD", "PILOT", "PASSPORT", "ODYSSEY"},
	"LEXUS":  {"RX", "NX", "ES", "UX"},
}

func modelTierScore(make, model string) int {
	top, ok := topModels[strings.ToUpper(make)]
	if !ok {
		return 0
	}
	m := strings.ToUpper(strings.TrimSpace(model))
	for _, t := range top {
		if strings.HasPrefix(m, t) {
			return 5
		}
	}
	return 0
}

type scoredLot struct {
	lot   copart.Lot
	total int
}

// percentileScore returns the score at the Nth percentile.
func percentileScore(all []scoredLot, pct int) int {
	if len(all) == 0 {
		return 0
	}
	scores := make([]int, len(all))
	for i, s := range all {
		scores[i] = s.total
	}
	for i := 1; i < len(scores); i++ {
		for j := i; j > 0 && scores[j] < scores[j-1]; j-- {
			scores[j], scores[j-1] = scores[j-1], scores[j]
		}
	}
	idx := len(scores) * pct / 100
	if idx >= len(scores) {
		idx = len(scores) - 1
	}
	return scores[idx]
}
