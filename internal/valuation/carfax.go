// Package valuation scrapes third-party sites for vehicle retail values.
package valuation

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// CarfaxResult holds values returned from the Carfax value tool.
type CarfaxResult struct {
	VIN             string `json:"vin"`
	NoAccidentValue int    `json:"no_accident_value"`   // Carfax "No Accident" history-based value
	WithHistory     int    `json:"with_history_value"`  // Carfax adjusted for reported accidents
	PrivateParty    int    `json:"private_party_value"` // private party estimate if shown
	TradeIn         int    `json:"trade_in_value"`
	Source          string `json:"source"` // "carfax"
	ScrapedAt       string `json:"scraped_at"`
	Error           string `json:"error,omitempty"`
}

// ScrapeCarfaxValue looks up a VIN at carfax.com/value/ and returns the values.
// zip defaults to "60601" (Chicago, neutral mid-US market) if empty.
func ScrapeCarfaxValue(vin, zip string) (*CarfaxResult, error) {
	if zip == "" {
		zip = "60601"
	}
	vin = strings.ToUpper(strings.TrimSpace(vin))
	if len(vin) != 17 {
		return nil, fmt.Errorf("invalid VIN length %d (need 17): %q", len(vin), vin)
	}

	u := launcher.New().Headless(true).MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.MustClose()

	page := browser.MustPage("https://www.carfax.com/value/")
	page.MustWaitLoad()
	time.Sleep(3 * time.Second)

	// Fill VIN
	vinInput, err := page.Element(
		`input[placeholder*='VIN'], input[aria-label*='VIN'], input[id*='vin'], input[name*='vin']`,
	)
	if err != nil {
		return nil, fmt.Errorf("VIN input not found: %w", err)
	}
	vinInput.MustClick()
	vinInput.MustInput(vin)
	time.Sleep(300 * time.Millisecond)

	// Fill zip
	zipInput, err := page.Element(
		`input[placeholder*='Zip'], input[placeholder*='zip'], input[aria-label*='zip'], input[aria-label*='Zip']`,
	)
	if err == nil {
		zipInput.MustClick()
		zipInput.MustInput(zip)
		time.Sleep(300 * time.Millisecond)
	}

	// Click submit
	submitBtn, err := page.ElementX(
		`//*[contains(text(),'Get CARFAX Value') or contains(text(),'Get Value')]`,
	)
	if err != nil {
		return nil, fmt.Errorf("submit button not found: %w", err)
	}
	submitBtn.MustClick()

	// Wait for result — page navigates to /value/results or similar
	time.Sleep(6 * time.Second)

	body := page.MustElement("body").MustText()
	result := &CarfaxResult{
		VIN:       vin,
		Source:    "carfax",
		ScrapedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// Parse dollar values from rendered text
	// Carfax shows: "No Accident\n$39,410" or "CARFAX History-Based Value\n$40,290"
	reVal := regexp.MustCompile(`\$([0-9,]+)`)
	reLabel := regexp.MustCompile(`(?i)(no.?accident|history.based value|private.party|trade.?in)[\s\S]{0,60}?\$([0-9,]+)`)

	for _, m := range reLabel.FindAllStringSubmatch(body, -1) {
		label := strings.ToLower(m[1])
		val := parseMoney(m[2])
		switch {
		case strings.Contains(label, "no") && strings.Contains(label, "accident"):
			result.NoAccidentValue = val
		case strings.Contains(label, "history"):
			if result.WithHistory == 0 {
				result.WithHistory = val
			}
		case strings.Contains(label, "private"):
			result.PrivateParty = val
		case strings.Contains(label, "trade"):
			result.TradeIn = val
		}
	}

	// Fallback: grab first two $ values if label parsing missed
	allVals := reVal.FindAllStringSubmatch(body, -1)
	if result.NoAccidentValue == 0 && len(allVals) >= 1 {
		// Largest value is likely the "no accident" / clean estimate
		max := 0
		for _, m := range allVals {
			if v := parseMoney(m[1]); v > max && v < 200000 {
				max = v
			}
		}
		if max > 0 {
			result.NoAccidentValue = max
		}
	}

	if result.NoAccidentValue == 0 {
		result.Error = "no value found in page — VIN may be unrecognised or page changed"
	}

	return result, nil
}

func parseMoney(s string) int {
	s = strings.ReplaceAll(s, ",", "")
	n, _ := strconv.Atoi(s)
	return n
}
