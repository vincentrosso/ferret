package damage

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const maxRetries = 5

const (
	apiURL    = "https://api.anthropic.com/v1/messages"
	model     = "claude-haiku-4-5-20251001"
	maxImages = 6
)

// Report is the output of a damage analysis pass.
type Report struct {
	LotNumber      string    `json:"lot_number"`
	Severity       string    `json:"severity"`        // light|moderate|heavy|severe
	AffectedPanels []string  `json:"affected_panels"`
	DentCountEst   int       `json:"dent_count_est"`
	PDRViable      bool      `json:"pdr_viable"`
	RepairCostLow  int       `json:"repair_cost_low"`
	RepairCostHigh int       `json:"repair_cost_high"`
	Notes          string    `json:"notes"`
	Confidence     string    `json:"confidence"` // low|medium|high
	AnalyzedAt     time.Time `json:"analyzed_at"`
	ImagesAnalyzed int       `json:"images_analyzed"`
}

// Analyzer calls Claude vision to assess hail damage from lot images.
type Analyzer struct {
	apiKey string
	client *http.Client
}

func New(apiKey string) *Analyzer {
	return &Analyzer{
		apiKey: apiKey,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// Analyze reads images from zipPath and returns a damage report for lotNumber.
func (a *Analyzer) Analyze(lotNumber, zipPath string) (*Report, error) {
	images, err := readImagesFromZip(zipPath, maxImages)
	if err != nil {
		return nil, fmt.Errorf("read images: %w", err)
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("no images in %s", zipPath)
	}

	raw, err := a.callClaude(images)
	if err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}

	// Strip markdown fences if Claude wraps the JSON
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.SplitN(raw, "\n", 2)
		if len(lines) == 2 {
			raw = lines[1]
		}
		raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
	}

	var report Report
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return nil, fmt.Errorf("parse response: %w\nraw: %s", err, raw)
	}
	report.LotNumber = lotNumber
	report.AnalyzedAt = time.Now()
	report.ImagesAnalyzed = len(images)
	return &report, nil
}

// callClaude sends images to Claude Haiku and returns the raw JSON text response.
func (a *Analyzer) callClaude(images [][]byte) (string, error) {
	// Build content blocks: images first, then the prompt text
	type imageSource struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	type contentBlock struct {
		Type   string       `json:"type"`
		Source *imageSource `json:"source,omitempty"`
		Text   string       `json:"text,omitempty"`
	}

	var content []contentBlock
	for _, img := range images {
		content = append(content, contentBlock{
			Type: "image",
			Source: &imageSource{
				Type:      "base64",
				MediaType: "image/jpeg",
				Data:      base64.StdEncoding.EncodeToString(img),
			},
		})
	}
	content = append(content, contentBlock{
		Type: "text",
		Text: damagePrompt,
	})

	body := map[string]any{
		"model":      model,
		"max_tokens": 512,
		"messages": []map[string]any{
			{"role": "user", "content": content},
		},
	}
	bodyB, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	var (
		resp  *http.Response
		respB []byte
	)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		r, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(bodyB))
		if err != nil {
			return "", err
		}
		r.Header.Set("x-api-key", a.apiKey)
		r.Header.Set("anthropic-version", "2023-06-01")
		r.Header.Set("content-type", "application/json")

		resp, err = a.client.Do(r)
		if err != nil {
			return "", err
		}
		respB, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			break
		}
		if attempt == maxRetries {
			return "", fmt.Errorf("API 429 after %d retries: %s", maxRetries, respB)
		}
		wait := time.Duration(1<<uint(attempt)) * 15 * time.Second // 15s, 30s, 60s, 120s, 240s
		time.Sleep(wait)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API %d: %s", resp.StatusCode, respB)
	}

	var apiResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respB, &apiResp); err != nil {
		return "", fmt.Errorf("unmarshal API response: %w", err)
	}
	for _, c := range apiResp.Content {
		if c.Type == "text" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("no text content in API response")
}

// readImagesFromZip extracts up to n JPEG images from a zip file, sorted by name.
func readImagesFromZip(zipPath string, n int) ([][]byte, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var files []*zip.File
	for _, f := range r.File {
		name := strings.ToLower(f.Name)
		if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	if len(files) > n {
		files = files[:n]
	}

	var out [][]byte
	for _, f := range files {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		out = append(out, data)
	}
	return out, nil
}

const damagePrompt = `You are a PDR (paintless dent repair) estimator assessing hail damage from auction photos.

Analyze these images and estimate repair costs using these benchmarks:
- light:    <50 small dents, PDR only           → $500–1,200
- moderate: 50–150 dents, mixed sizes, PDR ok   → $1,200–2,500
- heavy:    150+ dents, some large              → $2,500–5,000 (PDR + possible panel work)
- severe:   large/deep dents, broken glass/trim → $5,000+      (avoid — not PDR-able)

Respond with ONLY valid JSON, no markdown fences:
{
  "severity": "light|moderate|heavy|severe",
  "affected_panels": ["hood","roof","trunk","driver_front_door","passenger_front_door","driver_rear_door","passenger_rear_door","fenders","pillars"],
  "dent_count_est": 45,
  "pdr_viable": true,
  "repair_cost_low": 800,
  "repair_cost_high": 1500,
  "notes": "one short sentence",
  "confidence": "low|medium|high"
}`
