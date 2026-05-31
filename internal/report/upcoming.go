package report

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UpcomingLot is a row in the watchlist page.
type UpcomingLot struct {
	Lot
	DetailURL string // relative path to the deep-dive page, e.g. /ferret/lot-12345.html
}

// Verdict returns a short recommendation string based on score and damage.
func (u UpcomingLot) Verdict() string {
	sev := ""
	if u.Damage != nil {
		sev = strings.ToLower(u.Damage.Severity)
	}
	switch {
	case u.Score.Total >= 75 && (sev == "light" || sev == ""):
		return "BID"
	case u.Score.Total >= 60 && (sev == "light" || sev == "moderate" || sev == ""):
		return "WATCH"
	default:
		return "PASS"
	}
}

func (u UpcomingLot) VerdictColor() string {
	switch u.Verdict() {
	case "BID":
		return "#4ade80"
	case "WATCH":
		return "#fbbf24"
	default:
		return "#475569"
	}
}

func (u UpcomingLot) VerdictBg() string {
	switch u.Verdict() {
	case "BID":
		return "#14532d"
	case "WATCH":
		return "#451a03"
	default:
		return "#0f172a"
	}
}

// GenerateUpcoming writes the watchlist page to outPath.
func GenerateUpcoming(lots []UpcomingLot, outPath string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return upcomingTmpl.Execute(f, map[string]any{
		"Lots":      lots,
		"Generated": time.Now().Format("Jan 2, 2006 3:04 PM"),
	})
}

var upcomingTmpl = template.Must(template.New("upcoming").Funcs(funcMap).Parse(upcomingTemplate))

const upcomingTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Upcoming Auctions — Hail Arb</title>
<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #0f172a; color: #e2e8f0; padding: 24px 20px; }
a { text-decoration: none; color: inherit; }

.hdr { margin-bottom: 20px; }
.hdr h1 { font-size: 1.1rem; font-weight: 700; color: #f8fafc; margin-bottom: 3px; }
.hdr .sub { font-size: 0.72rem; color: #475569; }
.hdr .sub a { color: #3b82f6; }
.hdr .sub a:hover { text-decoration: underline; }

.list { display: flex; flex-direction: column; gap: 6px; }

.row { display: grid; grid-template-columns: 64px 48px 1fr auto; align-items: center; gap: 12px;
       background: #1e293b; border: 1px solid #334155; border-radius: 8px; padding: 10px 12px;
       transition: border-color .12s; }
.row:hover { border-color: #3b82f6; }

.thumb { width: 64px; height: 48px; border-radius: 4px; object-fit: cover; flex-shrink: 0; background: #0f172a; }
.no-thumb { width: 64px; height: 48px; border-radius: 4px; background: #0f172a; }

.verdict { display: flex; align-items: center; justify-content: center;
           font-size: 0.6rem; font-weight: 800; letter-spacing: .06em;
           border-radius: 5px; padding: 3px 6px; text-align: center; white-space: nowrap; }

.main { min-width: 0; }
.title { font-size: 0.88rem; font-weight: 600; color: #f1f5f9; white-space: nowrap;
         overflow: hidden; text-overflow: ellipsis; margin-bottom: 3px; }
.meta { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; }
.m { font-size: 0.68rem; color: #94a3b8; }
.m b { color: #e2e8f0; }
.sev { font-size: 0.62rem; font-weight: 700; padding: 1px 6px; border-radius: 99px; }
.score-wrap { display: flex; align-items: center; gap: 5px; }
.score-track { width: 60px; background: #0f172a; border-radius: 2px; height: 4px; }
.score-fill { height: 100%; border-radius: 2px; background: #3b82f6; }
.score-num { font-size: 0.65rem; color: #64748b; font-weight: 600; min-width: 22px; }

.actions { display: flex; flex-direction: column; gap: 5px; align-items: flex-end; flex-shrink: 0; }
.btn { display: inline-block; font-size: 0.68rem; font-weight: 600; padding: 4px 10px;
       border-radius: 5px; border: 1px solid #334155; color: #94a3b8; background: #0f172a;
       white-space: nowrap; }
.btn:hover { border-color: #3b82f6; color: #e2e8f0; }
.btn-copart { background: #1d4ed8; border-color: #2563eb; color: #fff; }
.btn-copart:hover { background: #2563eb; }

.footer { margin-top: 20px; font-size: 0.62rem; color: #334155; }

@media (max-width: 540px) {
  .row { grid-template-columns: 48px 36px 1fr; }
  .actions { display: none; }
}
</style>
</head>
<body>
<div class="hdr">
  <h1>Upcoming Auctions</h1>
  <div class="sub">{{.Generated}} &nbsp;·&nbsp; {{len .Lots}} lots tracked &nbsp;·&nbsp;
    <a href="/ferret/">Full reports →</a>
  </div>
</div>

<div class="list">
{{- range .Lots}}
<div class="row">
  {{- if .ThumbnailURL}}
  <img class="thumb" src="{{.ThumbnailURL | safeURL}}" loading="lazy">
  {{- else}}
  <div class="no-thumb"></div>
  {{- end}}

  <div class="verdict" style="background:{{.VerdictBg}};color:{{.VerdictColor}}">{{.Verdict}}</div>

  <div class="main">
    <div class="title">{{.Year}} {{.Make}} {{.Model}}</div>
    <div class="meta">
      <div class="score-wrap">
        <div class="score-track"><div class="score-fill" style="width:{{.Score.Total}}%"></div></div>
        <span class="score-num">{{.Score.Total}}</span>
      </div>
      {{- if .Damage}}
      <span class="sev" style="background:{{severityBg .Damage.Severity}};color:{{severityColor .Damage.Severity}}">{{.Damage.Severity}}</span>
      {{- if .Damage.PDRViable}}<span class="m" style="color:#4ade80">PDR ✓</span>{{end}}
      {{- end}}
      <span class="m"><b>{{dollar (ftoi .CurrentBid)}}</b> <span style="color:#475569">bid</span></span>
      <span class="m">{{commas .Odometer}} mi</span>
      {{- if .SaleDate}}<span class="m" style="color:#fbbf24;font-weight:600">{{.SaleDate}}</span>{{end}}
    </div>
  </div>

  <div class="actions">
    <a class="btn btn-copart" href="{{.LotURL | safeURL}}" target="_blank">Copart ↗</a>
    {{- if .DetailURL}}
    <a class="btn" href="{{.DetailURL | safeURL}}">Deep Dive →</a>
    {{- end}}
  </div>
</div>
{{- end}}
</div>

<div class="footer">autoarb.ndex.us · ferret hail-arb scanner</div>
</body>
</html>`
