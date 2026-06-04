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
	Makes       []string  // e.g. ["TOYOTA","HONDA"] — defaults to common hail-arb makes
	YearMin     int       // inclusive; defaults to 4 years ago
	YearMax     int       // inclusive; defaults to current year + 2
	OdoMax      int       // max odometer; defaults to 85 000
	DateFrom    time.Time // auction start; defaults to today
	DateTo      time.Time // auction end; defaults to today + 5 days
	DamageCode  string    // Copart filter code; defaults to DAMAGECODE_HL (hail)
	TitleGroups []string  // one or more title group codes; defaults to [TITLEGROUP_C]
	// known values: TITLEGROUP_C (clean), TITLEGROUP_S (salvage)
	Sort     []string // Solr sort fields; defaults to Copart's standard date sort
	MaxPages int      // 0 = unlimited
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
	EstRetail     float64 // Copart "Est. Retail Value" (ACV) from the search row
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
	reLotNum   = regexp.MustCompile(`/lot/(\d+)`)
	reMoney    = regexp.MustCompile(`[\d,]+(?:\.\d+)?`)
	reOdoText  = regexp.MustCompile(`^([\d,]+)\s*[A-Z]?$`)
	reSaleTime = regexp.MustCompile(`(?i)\d{1,2}:\d{2}\s*(AM|PM)`) // auction time cell, e.g. "10:00 AM PDT"
)

// colMap resolves a logical field to its column index in the classic results
// table. Copart periodically reorders the search columns (and has — a prior
// fixed-index map silently read Title out of the Damage column), so we resolve
// by header text rather than trusting fixed positions.
type colMap map[string]int

// buildColMap derives column indices from the results-table header row.
// Headers seen on the classic (#serverSideDataTable) view, 2026-06:
//
//	["", Images, Lot #, Year, Make, Model, Item#, Location / Lane,
//	 Sale Date, Odometer, Title Code, Damage, Est. Retail Value, Current Bid, ""]
func buildColMap(headers []string) colMap {
	m := colMap{}
	for i, h := range headers {
		h = strings.ToLower(strings.TrimSpace(h))
		switch {
		case strings.Contains(h, "year"):
			m["year"] = i
		case strings.Contains(h, "make"):
			m["make"] = i
		case strings.Contains(h, "model"):
			m["model"] = i
		case strings.Contains(h, "odom"):
			m["odometer"] = i
		case strings.Contains(h, "title"):
			m["title"] = i
		case strings.Contains(h, "retail"):
			m["retail"] = i // checked before "damage"/"bid" — "Est. Retail Value"
		case strings.Contains(h, "damage"):
			m["damage"] = i
		case strings.Contains(h, "bid"):
			m["bid"] = i
		case strings.Contains(h, "location"), strings.Contains(h, "lane"):
			m["location"] = i
		case strings.Contains(h, "sale") && strings.Contains(h, "date"):
			m["saledate"] = i
		}
	}
	return m
}

// legacyCols is the fallback index map matching the current classic layout,
// used only when the header row can't be read (cols comes back empty).
var legacyCols = colMap{
	"year": 3, "make": 4, "model": 5, "location": 7, "saledate": 8,
	"odometer": 9, "title": 10, "damage": 11, "retail": 12, "bid": 13,
}

// parseLotRow extracts a Lot from a Copart classic-view search result row,
// resolving cells through cols (header-derived). Pass an empty cols to fall back
// to the known fixed layout.
func parseLotRow(lotURL string, cells []string, cols colMap, now time.Time) (Lot, bool) {
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

	if len(cols) == 0 {
		cols = legacyCols
	}
	// cell returns the trimmed text of the column mapped to key, or "" if absent.
	cell := func(key string) string {
		i, ok := cols[key]
		if !ok || i >= len(cells) {
			return ""
		}
		return strings.TrimSpace(cells[i])
	}

	if y, err := strconv.Atoi(cell("year")); err == nil && y > 1990 {
		lot.Year = y
	}
	lot.Make = strings.ToUpper(strings.TrimSpace(cell("make")))
	lot.Model = strings.TrimSpace(cell("model"))

	if lot.Year > 0 && lot.Make != "" {
		lot.Title = fmt.Sprintf("%d %s %s", lot.Year, lot.Make, lot.Model)
	}
	if lot.Make == "" {
		return Lot{}, false
	}

	// Odometer: "81946 A" or "81,946 A" → 81946
	if raw := cell("odometer"); raw != "" {
		if m := reOdoText.FindStringSubmatch(strings.TrimSpace(strings.Split(raw, "\n")[0])); len(m) >= 2 {
			if n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", "")); err == nil {
				lot.Odometer = n
			}
		}
	}

	// Title Code (e.g. "CT - TX") → TitleType="CT". The trailing token is the
	// title's issuing state, not the yard — the yard comes from Location / Lane.
	if t := cell("title"); t != "" {
		lot.TitleType = strings.TrimSpace(strings.SplitN(t, "-", 2)[0])
	}

	// Location / Lane (e.g. "TX - FT. WORTH -") → yard, trailing separators trimmed.
	if loc := cell("location"); loc != "" {
		lot.YardName = strings.TrimRight(strings.TrimSpace(strings.Split(loc, "\n")[0]), " -")
	}

	// Sale date: prefer the Sale Date column when it carries a real date/time
	// ("Future" is a placeholder), else scan any cell that looks like an auction
	// time ("10:00 AM PDT"). The detail scrape overrides this authoritatively.
	if sd := cell("saledate"); sd != "" && !strings.EqualFold(sd, "future") {
		lot.SaleDate = strings.TrimSpace(strings.Split(sd, "\n")[0])
	} else {
		for _, c := range cells {
			c = strings.TrimSpace(strings.Split(c, "\n")[0])
			if reSaleTime.MatchString(c) {
				lot.SaleDate = c
				break
			}
		}
	}

	lot.DamagePrimary = cell("damage")

	// Est. Retail Value (ACV), e.g. "$27,822"
	if r := cell("retail"); r != "" {
		if m := reMoney.FindString(r); m != "" {
			if f, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", ""), 64); err == nil {
				lot.EstRetail = f
			}
		}
	}

	// Current bid: "Current bid : $1,234.00\nBid now"
	if b := cell("bid"); b != "" {
		if m := reMoney.FindString(b); m != "" {
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
