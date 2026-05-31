package report

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vincentrosso/ferret/internal/damage"
	"github.com/vincentrosso/ferret/internal/scoring"
)

// Lot is the combined data passed to the report template.
type Lot struct {
	scoring.RankedLot
	Damage        *damage.Report
	TotalCostLow  int
	TotalCostHigh int
	EstResale     int
	MaxBid        int // max bid for 50% ROI
	ROIAt80Pct    int // ROI% if bid at 80% of MaxBid
}

// Generate writes a top-N HTML report to outPath.
func Generate(lots []Lot, outPath string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return tmpl.Execute(f, map[string]any{
		"Lots":      lots,
		"Generated": time.Now().Format("Jan 2, 2006 3:04 PM"),
		"Date":      time.Now().Format("2006-01-02"),
	})
}

func severityColor(s string) string {
	switch strings.ToLower(s) {
	case "light":
		return "#22c55e"
	case "moderate":
		return "#f59e0b"
	case "heavy", "severe":
		return "#ef4444"
	default:
		return "#94a3b8"
	}
}

func severityBg(s string) string {
	switch strings.ToLower(s) {
	case "light":
		return "#dcfce7"
	case "moderate":
		return "#fef3c7"
	case "heavy", "severe":
		return "#fee2e2"
	default:
		return "#f1f5f9"
	}
}

func dollar(n int) string {
	if n <= 0 {
		return "$0"
	}
	return "$" + commas(n)
}

func commas(n int) string {
	s := fmt.Sprintf("%d", n)
	var b strings.Builder
	off := len(s) % 3
	for i, c := range s {
		if i > 0 && i%3 == off {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func pct(val, max int) int {
	if max == 0 {
		return 0
	}
	p := val * 100 / max
	if p > 100 {
		return 100
	}
	return p
}

var funcMap = template.FuncMap{
	"severityColor": severityColor,
	"severityBg":    severityBg,
	"dollar":        dollar,
	"commas":        commas,
	"pct":           pct,
	"join":          strings.Join,
	"add1":          func(i int) int { return i + 1 },
	"ftoi":          func(f float64) int { return int(f) },
	"ge":            func(a, b int) bool { return a >= b },
	"safeURL":       func(s string) template.URL { return template.URL(s) },
}

var tmpl = template.Must(template.New("report").Funcs(funcMap).Parse(htmlTemplate))

const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Hail Arb — {{.Date}}</title>
<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #0f172a; color: #e2e8f0; padding: 24px; }
h1 { font-size: 1.4rem; font-weight: 700; color: #f8fafc; margin-bottom: 4px; }
.meta { font-size: 0.78rem; color: #64748b; margin-bottom: 24px; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(460px, 1fr)); gap: 16px; }
.card { background: #1e293b; border-radius: 12px; overflow: hidden; border: 1px solid #334155; }
.card-header { display: flex; }
.thumb { width: 170px; min-height: 128px; object-fit: cover; flex-shrink: 0; background: #0f172a; }
.no-thumb { width: 170px; min-height: 128px; background: #0f172a; display: flex; align-items: center; justify-content: center; color: #475569; font-size: 0.7rem; flex-shrink: 0; }
.card-top { padding: 12px 14px; flex: 1; min-width: 0; }
.badges { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; flex-wrap: wrap; }
.rank { font-size: 0.7rem; color: #64748b; font-weight: 700; }
.score-badge { background: #0f172a; border: 1px solid #334155; border-radius: 5px; padding: 1px 7px; font-size: 0.7rem; color: #94a3b8; }
.score-badge b { color: #f8fafc; font-size: 0.85rem; }
.sev-pill { display: inline-block; padding: 1px 8px; border-radius: 99px; font-size: 0.65rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.05em; }
.car-title { font-size: 0.95rem; font-weight: 700; color: #f1f5f9; line-height: 1.3; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.lot-link { font-size: 0.68rem; color: #3b82f6; text-decoration: none; }
.lot-link:hover { text-decoration: underline; }
.stats { padding: 0 14px 12px; display: grid; grid-template-columns: 1fr 1fr; gap: 6px 14px; }
.stat-label { font-size: 0.6rem; text-transform: uppercase; letter-spacing: 0.05em; color: #64748b; }
.stat-value { font-size: 0.85rem; font-weight: 600; color: #e2e8f0; }
.notes { padding: 0 14px 10px; font-size: 0.7rem; color: #94a3b8; line-height: 1.5; border-top: 1px solid #263347; padding-top: 8px; }
.bars { padding: 0 14px 12px; }
.bar-row { display: flex; align-items: center; gap: 7px; margin-bottom: 3px; }
.bar-label { width: 56px; font-size: 0.62rem; color: #64748b; flex-shrink: 0; }
.bar-track { flex: 1; background: #0f172a; border-radius: 3px; height: 5px; overflow: hidden; }
.bar-fill { height: 100%; border-radius: 3px; background: #3b82f6; }
.bar-num { width: 20px; text-align: right; font-size: 0.62rem; color: #64748b; flex-shrink: 0; }
.financials { padding: 10px 14px; background: #0f172a; border-top: 1px solid #334155; display: grid; grid-template-columns: 1fr 1fr; gap: 8px 12px; }
.fin-item { display: flex; flex-direction: column; }
.fin-label { font-size: 0.6rem; text-transform: uppercase; letter-spacing: 0.05em; color: #64748b; margin-bottom: 1px; }
.fin-value { font-size: 0.9rem; font-weight: 700; }
.fin-green { color: #22c55e; }
.fin-blue { color: #60a5fa; }
.fin-amber { color: #f59e0b; }
.fin-red { color: #ef4444; }
</style>
</head>
<body>
<h1>Hail Arb — Top {{len .Lots}}</h1>
<p class="meta">{{.Generated}} &nbsp;·&nbsp; 5-day auction window &nbsp;·&nbsp; Toyota · Honda · Lexus</p>
<div class="grid">
{{- range $i, $l := .Lots}}
<div class="card">
  <div class="card-header">
    {{- if $l.ThumbnailURL}}
    <img class="thumb" src="{{$l.ThumbnailURL | safeURL}}" alt="{{$l.Title}}" loading="lazy">
    {{- else}}
    <div class="no-thumb">no image</div>
    {{- end}}
    <div class="card-top">
      <div class="badges">
        <span class="rank">#{{add1 $i}}</span>
        <span class="score-badge">Score <b>{{$l.Score.Total}}</b>/100</span>
        {{- if $l.Damage}}
        <span class="sev-pill" style="background:{{severityBg $l.Damage.Severity}};color:{{severityColor $l.Damage.Severity}}">{{$l.Damage.Severity}}</span>
        {{- end}}
      </div>
      <div class="car-title">{{$l.Year}} {{$l.Make}} {{$l.Model}}</div>
      <div style="display:flex;gap:10px;align-items:center;margin-top:1px">
        <a class="lot-link" href="{{$l.LotURL}}" target="_blank">Lot #{{$l.LotNumber}} ↗</a>
        <a href="lot-{{$l.LotNumber}}.html" style="font-size:0.68rem;color:#4ade80;font-weight:600">Deep Dive →</a>
      </div>
    </div>
  </div>

  <div class="stats">
    <div>
      <div class="stat-label">Current Bid</div>
      <div class="stat-value">{{dollar (ftoi $l.CurrentBid)}}</div>
    </div>
    <div>
      <div class="stat-label">Mileage</div>
      <div class="stat-value">{{commas $l.Odometer}} mi</div>
    </div>
    {{- if $l.Damage}}
    <div>
      <div class="stat-label">Repair Estimate</div>
      <div class="stat-value">{{dollar $l.Damage.RepairCostLow}} – {{dollar $l.Damage.RepairCostHigh}}</div>
    </div>
    <div>
      <div class="stat-label">PDR Viable</div>
      <div class="stat-value">{{if $l.Damage.PDRViable}}✓ Yes{{else}}✗ No{{end}}</div>
    </div>
    {{- end}}
    {{- if $l.SaleDate}}
    <div>
      <div class="stat-label">Sale Date</div>
      <div class="stat-value">{{$l.SaleDate}}</div>
    </div>
    {{- end}}
    <div>
      <div class="stat-label">Title</div>
      <div class="stat-value">{{$l.TitleType}}</div>
    </div>
  </div>

  {{- if $l.Damage}}
  <div class="notes">
    {{- if $l.Damage.AffectedPanels}}<b>Panels:</b> {{join $l.Damage.AffectedPanels ", "}} &nbsp;·&nbsp; {{end -}}
    ~{{$l.Damage.DentCountEst}} dents &nbsp;·&nbsp; confidence: {{$l.Damage.Confidence}}<br>
    {{$l.Damage.Notes}}
  </div>
  {{- end}}

  <div class="bars">
    <div class="bar-row"><span class="bar-label">damage</span><div class="bar-track"><div class="bar-fill" style="width:{{pct $l.Score.Damage 30}}%"></div></div><span class="bar-num">{{$l.Score.Damage}}</span></div>
    <div class="bar-row"><span class="bar-label">make</span><div class="bar-track"><div class="bar-fill" style="width:{{pct $l.Score.Make 20}}%"></div></div><span class="bar-num">{{$l.Score.Make}}</span></div>
    <div class="bar-row"><span class="bar-label">mileage</span><div class="bar-track"><div class="bar-fill" style="width:{{pct $l.Score.Mileage 20}}%"></div></div><span class="bar-num">{{$l.Score.Mileage}}</span></div>
    <div class="bar-row"><span class="bar-label">year</span><div class="bar-track"><div class="bar-fill" style="width:{{pct $l.Score.Year 15}}%"></div></div><span class="bar-num">{{$l.Score.Year}}</span></div>
    <div class="bar-row"><span class="bar-label">bid</span><div class="bar-track"><div class="bar-fill" style="width:{{pct $l.Score.Bid 10}}%"></div></div><span class="bar-num">{{$l.Score.Bid}}</span></div>
    <div class="bar-row"><span class="bar-label">model</span><div class="bar-track"><div class="bar-fill" style="width:{{pct $l.Score.ModelTier 5}}%"></div></div><span class="bar-num">{{$l.Score.ModelTier}}</span></div>
  </div>

  {{- if $l.MaxBid}}
  <div class="financials">
    <div class="fin-item">
      <span class="fin-label">Est. Resale</span>
      <span class="fin-value fin-blue">{{dollar $l.EstResale}}</span>
    </div>
    <div class="fin-item">
      <span class="fin-label">Max Bid (50% ROI)</span>
      <span class="fin-value fin-amber">{{dollar $l.MaxBid}}</span>
    </div>
    <div class="fin-item">
      <span class="fin-label">Total In (bid + repair)</span>
      <span class="fin-value fin-green">{{dollar $l.TotalCostLow}} – {{dollar $l.TotalCostHigh}}</span>
    </div>
    <div class="fin-item">
      <span class="fin-label">ROI @ 80% Max Bid</span>
      <span class="fin-value {{if ge $l.ROIAt80Pct 50}}fin-green{{else if ge $l.ROIAt80Pct 25}}fin-amber{{else}}fin-red{{end}}">{{$l.ROIAt80Pct}}%</span>
    </div>
  </div>
  {{- end}}
</div>
{{- end}}
</div>
</body>
</html>`
