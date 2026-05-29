package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// LotRecord is the in-memory view of a scraped lot (detail.json).
type LotRecord struct {
	LotNumber          string       `json:"lot_number"`
	LotURL             string       `json:"lot_url"`
	VIN                string       `json:"vin,omitempty"`
	DamagePrimary      string       `json:"damage_primary,omitempty"`
	DamageSecondary    string       `json:"damage_secondary,omitempty"`
	RunAndDrive        string       `json:"run_and_drive,omitempty"`
	ConditionGrade     string       `json:"condition_grade,omitempty"`
	EngineType         string       `json:"engine_type,omitempty"`
	Transmission       string       `json:"transmission,omitempty"`
	Color              string       `json:"color,omitempty"`
	BodyStyle          string       `json:"body_style,omitempty"`
	Odometer           int          `json:"odometer,omitempty"`
	DriveType          string       `json:"drive_type,omitempty"`
	FuelType           string       `json:"fuel_type,omitempty"`
	LossType           string       `json:"loss_type,omitempty"`
	KeysPresent        string       `json:"keys_present,omitempty"`
	AirbagsDeployed    string       `json:"airbags_deployed,omitempty"`
	ExteriorCondition  []DamageItem `json:"exterior_condition,omitempty"`
	VehicleDetailsText string       `json:"vehicle_details_text,omitempty"`
	ImageURLs          []string     `json:"image_urls,omitempty"`
	ImageZip           string       `json:"image_zip,omitempty"`
	CurrentBid         float64      `json:"current_bid,omitempty"`
	FinalBid           float64      `json:"final_bid,omitempty"`
	SaleStatus         string       `json:"sale_status,omitempty"`
	SaleDate           string       `json:"sale_date,omitempty"`
	IsBIN              bool         `json:"is_bin,omitempty"`
	BuyNowAmount       float64      `json:"buy_now_amount,omitempty"`
	ScrapedAt          time.Time    `json:"scraped_at"`

	// Enriched from search JSON (lots-*.json) if available
	Title     string `json:"title,omitempty"`
	Year      int    `json:"year,omitempty"`
	Make      string `json:"make,omitempty"`
	Model     string `json:"model,omitempty"`
	TitleType string `json:"title_type,omitempty"`
	YardName  string `json:"yard_name,omitempty"`
}

type DamageItem struct {
	Panel  string `json:"panel"`
	Damage string `json:"damage"`
	Count  string `json:"count"`
}

// MemStore holds all scraped lot records in memory.
type MemStore struct {
	mu      sync.RWMutex
	lots    map[string]*LotRecord // keyed by lot number
	dataDir string
}

func NewMemStore(dataDir string) *MemStore {
	return &MemStore{dataDir: dataDir, lots: make(map[string]*LotRecord)}
}

// Load reads all data/raw/*/detail.json files into memory.
func (m *MemStore) Load() (int, error) {
	pattern := filepath.Join(m.dataDir, "raw", "*", "detail.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return 0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rec LotRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			continue
		}
		m.lots[rec.LotNumber] = &rec
	}
	return len(m.lots), nil
}

// All returns all lots sorted by scraped_at descending.
func (m *MemStore) All() []*LotRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*LotRecord, 0, len(m.lots))
	for _, l := range m.lots {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ScrapedAt.After(out[j].ScrapedAt)
	})
	return out
}

// Get returns a single lot by number.
func (m *MemStore) Get(lotNumber string) (*LotRecord, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l, ok := m.lots[lotNumber]
	return l, ok
}

// Count returns total number of loaded lots.
func (m *MemStore) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.lots)
}
