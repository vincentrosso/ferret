package copart

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SearchParams controls what the Copart search URL filters on.
// Zero values produce sensible defaults (hail damage, clean title, past 14 days).
type SearchParams struct {
	Makes      []string  // e.g. ["TOYOTA","HONDA"] — defaults to common hail-arb makes
	YearMin    int       // inclusive; defaults to 4 years ago
	YearMax    int       // inclusive; defaults to current year + 2
	OdoMax     int       // max odometer; defaults to 85 000
	DateFrom   time.Time // auction start; defaults to today
	DateTo     time.Time // auction end; defaults to today + 5 days
	DamageCode  string   // Copart filter code; defaults to DAMAGECODE_HL (hail)
	TitleGroups []string // one or more title group codes; defaults to [TITLEGROUP_C]
	             	     // known values: TITLEGROUP_C (clean), TITLEGROUP_S (salvage)
	Sort        []string // Solr sort fields; defaults to Copart's standard date sort
	MaxPages    int      // 0 = unlimited
}

func (p *SearchParams) defaults() {
	now := time.Now()
	if len(p.Makes) == 0 {
		p.Makes = []string{"TOYOTA", "HONDA", "LEXUS"}
	}
	if p.YearMin == 0 {
		p.YearMin = now.Year() - 4
	}
	if p.YearMax == 0 {
		p.YearMax = now.Year() + 1
	}
	if p.OdoMax == 0 {
		p.OdoMax = 85_000
	}
	if p.DateFrom.IsZero() {
		p.DateFrom = now
	}
	if p.DateTo.IsZero() {
		p.DateTo = now.AddDate(0, 0, 5)
	}
	if p.DamageCode == "" {
		p.DamageCode = "DAMAGECODE_HL"
	}
	if len(p.TitleGroups) == 0 {
		p.TitleGroups = []string{"TITLEGROUP_C"}
	}
	if len(p.Sort) == 0 {
		p.Sort = []string{"auction_date_type desc", "auction_date_utc asc"}
	}
}

// BuildURL constructs the Copart lotSearchResults URL for the given params.
func (p SearchParams) BuildURL() (string, error) {
	p.defaults()

	makes := make([]string, len(p.Makes))
	for i, m := range p.Makes {
		makes[i] = fmt.Sprintf(`lot_make_desc:"%s"`, strings.ToUpper(m))
	}

	criteria := map[string]any{
		"query": []string{"*"},
		"filter": map[string][]string{
			"MAKE": makes,
			"MISC": {"#VehicleTypeCode:VEHTYPE_V"},
			"ODM":  {fmt.Sprintf("odometer_reading_received:[0 TO %d]", p.OdoMax)},
			"PRID": {fmt.Sprintf("damage_type_code:%s", p.DamageCode)},
			"SDAT": {fmt.Sprintf(
				`auction_date_utc:["%sT00:00:00Z" TO "%sT23:59:59Z"]`,
				p.DateFrom.Format("2006-01-02"),
				p.DateTo.Format("2006-01-02"),
			)},
			"TITL": titleFilters(p.TitleGroups),
			"VEHT": {"vehicle_type_code:VEHTYPE_V"},
			"YEAR": {fmt.Sprintf("lot_year:[%d TO %d]", p.YearMin, p.YearMax)},
		},
		"sort":           p.Sort,
		"watchListOnly":  false,
		"searchName":     "",
		"freeFormSearch": false,
	}

	b, err := json.Marshal(criteria)
	if err != nil {
		return "", err
	}

	return baseURL + "/lotSearchResults?free=false" +
		"&searchCriteria=" + url.QueryEscape(string(b)) +
		"&from=%2FvehicleFinder&fromSource=widget", nil
}

// ── Lot (search result) ───────────────────────────────────────────────────

// Lot holds the fields extractable from a Copart search result row.
type Lot struct {
	LotNumber     string
	LotURL        string
	Title         string
	Year          int
	Make          string
	Model         string
	DamagePrimary string
	Odometer      int
	TitleType     string
	YardName      string
	SaleDate      string
	CurrentBid    float64
	ThumbnailURL  string
	IsHail        bool
	IsSalvage     bool
	ScrapedAt     time.Time
}

// titleFilters builds the TITL filter slice from one or more group codes.
// Short aliases (C, S) are expanded to their full code.
func titleFilters(groups []string) []string {
	aliases := map[string]string{"C": "TITLEGROUP_C", "S": "TITLEGROUP_S"}
	out := make([]string, len(groups))
	for i, g := range groups {
		g = strings.ToUpper(strings.TrimSpace(g))
		if full, ok := aliases[g]; ok {
			g = full
		}
		out[i] = "title_group_code:" + g
	}
	return out
}

// ── Row parsing helpers ───────────────────────────────────────────────────

var (
	reLotNum  = regexp.MustCompile(`/lot/(\d+)`)
	reMoney   = regexp.MustCompile(`[\d,]+(?:\.\d+)?`)
	reOdoText = regexp.MustCompile(`^([\d,]+)\s*[A-Z]?$`)
)

// parseLotRow extracts a Lot from the Copart 2025 search result row (15 columns).
//
// Actual column layout observed via DOM inspection:
//   [0]  checkbox (empty)
//   [1]  thumbnail image
//   [2]  lot number + "Watch" link
//   [3]  year
//   [4]  make
//   [5]  model
//   [6]  ? (often "0")
//   [7]  damage code short (e.g. "HL")
//   [8]  sale status / lane (e.g. "Future")
//   [9]  odometer (e.g. "81946 A")
//   [10] auction time (e.g. "10:00 AM PDT")
//   [11] title type + state (e.g. "CT - KS")
//   [12] damage description (e.g. "HAIL")
//   [13] current bid (e.g. "Current bid : $0.00\nBid now")
//   [14] (empty)
func parseLotRow(lotURL string, cells []string, now time.Time) (Lot, bool) {
	lotM := reLotNum.FindStringSubmatch(lotURL)
	if len(lotM) < 2 {
		return Lot{}, false
	}

	// Clean up URLs that come back as "https://www.copart.com./lot/..."
	cleanURL := strings.Replace(lotURL, "copart.com./", "copart.com/", 1)

	lot := Lot{
		LotNumber: lotM[1],
		LotURL:    cleanURL,
		ScrapedAt: now,
	}

	cell := func(i int) string {
		if i < len(cells) {
			return strings.TrimSpace(cells[i])
		}
		return ""
	}

	// Year (col 3)
	if y, err := strconv.Atoi(cell(3)); err == nil && y > 1990 {
		lot.Year = y
	}

	// Make (col 4)
	lot.Make = strings.ToUpper(strings.TrimSpace(cell(4)))

	// Model (col 5)
	lot.Model = strings.TrimSpace(cell(5))

	// Title (composed)
	if lot.Year > 0 && lot.Make != "" {
		lot.Title = fmt.Sprintf("%d %s %s", lot.Year, lot.Make, lot.Model)
	}

	if lot.Make == "" {
		return Lot{}, false
	}

	// Odometer (col 9): "81946 A" or "81,946 A" → 81946
	if raw := cell(9); raw != "" {
		if m := reOdoText.FindStringSubmatch(strings.TrimSpace(strings.Split(raw, "\n")[0])); len(m) >= 2 {
			if n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", "")); err == nil {
				lot.Odometer = n
			}
		}
	}

	// Title type + state (col 11): "CT - KS" → TitleType="CT", YardName=state abbr
	if c11 := cell(11); c11 != "" {
		parts := strings.SplitN(c11, "-", 2)
		lot.TitleType = strings.TrimSpace(parts[0])
		if len(parts) > 1 {
			lot.YardName = strings.TrimSpace(parts[1])
		}
	}

	// Damage description (col 12): "HAIL"
	lot.DamagePrimary = strings.TrimSpace(cell(12))

	// Current bid (col 13): "Current bid : $1,234.00\nBid now"
	if c13 := cell(13); c13 != "" {
		if m := reMoney.FindString(c13); m != "" {
			if f, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", ""), 64); err == nil {
				lot.CurrentBid = f
			}
		}
	}

	lot.IsHail = strings.Contains(strings.ToLower(lot.DamagePrimary), "hail")
	lot.IsSalvage = strings.Contains(strings.ToLower(lot.TitleType), "salvage") ||
		strings.Contains(strings.ToLower(lot.TitleType), "sv")

	return lot, true
}

