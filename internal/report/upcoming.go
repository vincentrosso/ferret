package report

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// UpcomingLot is a row in the watchlist page.
type UpcomingLot struct {
	Lot
	DetailURL string // relative path to the deep-dive page, e.g. /ferret/lot-12345.html
}

// saleTime parses the lot's "MM/DD/YYYY" sale date, or zero time if absent/bad.
func (u UpcomingLot) saleTime() time.Time {
	t, err := time.Parse("01/02/2006", strings.TrimSpace(u.SaleDate))
	if err != nil {
		return time.Time{}
	}
	return t
}

// DaysUntil returns whole days until the auction (negative if past, -999 if unknown).
func (u UpcomingLot) DaysUntil() int {
	t := u.saleTime()
	if t.IsZero() {
		return -999
	}
	today := time.Now().Truncate(24 * time.Hour)
	return int(t.Truncate(24*time.Hour).Sub(today).Hours() / 24)
}

// Countdown is a human label for how soon the auction is.
func (u UpcomingLot) Countdown() string {
	d := u.DaysUntil()
	switch {
	case d == -999:
		return "date TBD"
	case d < 0:
		return "past"
	case d == 0:
		return "TODAY"
	case d == 1:
		return "TOMORROW"
	case d <= 7:
		return fmt.Sprintf("in %d days", d)
	default:
		return u.SaleDate
	}
}

// DayLabel groups lots into per-day sections on the upcoming page — a friendly date
// with a Today/Tomorrow/in-N-days tag. Unknown dates bucket last.
func (u UpcomingLot) DayLabel() string {
	d := u.DaysUntil()
	if d == -999 {
		return "Sale date TBD"
	}
	rel := ""
	switch {
	case d == 0:
		rel = " · Today"
	case d == 1:
		rel = " · Tomorrow"
	case d <= 7:
		rel = fmt.Sprintf(" · in %d days", d)
	}
	return u.saleTime().Format("Mon, Jan 2") + rel
}

// CountdownColor highlights imminent auctions.
func (u UpcomingLot) CountdownColor() string {
	d := u.DaysUntil()
	switch {
	case d == 0:
		return "#f87171" // today — red hot
	case d == 1:
		return "#fb923c" // tomorrow — orange
	case d >= 2 && d <= 3:
		return "#fbbf24" // soon — amber
	default:
		return "#94a3b8"
	}
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

// GenerateUpcoming writes the curated list: soonest auctions worth acting on,
// nearest-date first. Drops PASS verdicts and clearly-past auctions; lots with
// no known sale date sink to the bottom (still shown, flagged "date TBD").
func GenerateUpcoming(lots []UpcomingLot, outPath string) error {
	var keep []UpcomingLot
	for _, l := range lots {
		if l.Verdict() == "PASS" {
			continue // not worth observing or bidding
		}
		if l.DaysUntil() >= 0 || l.DaysUntil() == -999 {
			keep = append(keep, l) // future or date-unknown; drop only definite past
		}
	}

	// Sort: soonest first. Known future dates ascend; unknown-date lots last.
	// Within the same day, better verdict (BID > WATCH) then higher score first.
	vrank := map[string]int{"BID": 0, "WATCH": 1, "PASS": 2}
	sort.SliceStable(keep, func(i, j int) bool {
		di, dj := keep[i].DaysUntil(), keep[j].DaysUntil()
		ui, uj := di == -999, dj == -999
		if ui != uj {
			return !ui // known dates before unknown
		}
		if !ui && di != dj {
			return di < dj // soonest first
		}
		if vrank[keep[i].Verdict()] != vrank[keep[j].Verdict()] {
			return vrank[keep[i].Verdict()] < vrank[keep[j].Verdict()]
		}
		return keep[i].Score.Total > keep[j].Score.Total
	})

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	// Group the sorted lots into per-day sections — "best deals for the day". keep is
	// already sorted soonest-day-first and best-deal-first within a day (verdict then
	// score, above), so consecutive same-day lots form each group in rank order.
	type dayGroup struct {
		Label string
		Count int
		Lots  []UpcomingLot
	}
	var groups []dayGroup
	for _, l := range keep {
		lbl := l.DayLabel()
		if len(groups) == 0 || groups[len(groups)-1].Label != lbl {
			groups = append(groups, dayGroup{Label: lbl})
		}
		g := &groups[len(groups)-1]
		g.Lots = append(g.Lots, l)
		g.Count++
	}

	return upcomingTmpl.Execute(f, map[string]any{
		"Groups":    groups,
		"Total":     len(keep),
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
  <h1>Best Deals by Sale Day</h1>
  <div class="sub">{{.Generated}} &nbsp;·&nbsp; {{.Total}} deals to watch &amp; bid &nbsp;·&nbsp;
    <a href="/ferret/">Full reports →</a> &nbsp;·&nbsp; <a href="/watcher.html">Bid watcher →</a>
  </div>
</div>

<div class="list">
{{- if not .Groups}}
<div style="padding:40px;text-align:center;color:#475569;font-size:0.85rem">No actionable deals with upcoming auction dates right now.</div>
{{- end}}
{{- range .Groups}}
<div style="margin:22px 0 8px;color:#f59e0b;font-weight:800;font-size:0.95rem;border-bottom:1px solid #334155;padding-bottom:5px">{{.Label}} <span style="color:#64748b;font-weight:600;font-size:0.76rem">· {{.Count}} deal{{if ne .Count 1}}s{{end}}</span></div>
{{- range .Lots}}
<div class="row">
  {{- if .ThumbnailURL}}
  <img class="thumb" src="{{.ThumbnailURL | safeURL}}" loading="lazy">
  {{- else}}
  <div class="no-thumb"></div>
  {{- end}}

  <div class="verdict" style="background:{{.VerdictBg}};color:{{.VerdictColor}}">{{.Verdict}}</div>

  <div class="main">
    <div class="title">
      <span style="color:{{.CountdownColor}};font-weight:800">⏰ {{.Countdown}}</span>
      &nbsp; {{.Year}} {{.Make}} {{.Model}}
    </div>
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
      {{- if .MaxBid}}<span class="m">max <b style="color:#4ade80">{{dollar .MaxBid}}</b></span>{{end}}
      <span class="m">{{commas .Odometer}} mi</span>
      {{- if .SaleDate}}<span class="m" style="color:#64748b">{{.SaleDate}}</span>{{end}}
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
{{- end}}
</div>

<div class="footer">autoarb.ndex.us · ferret hail-arb scanner</div>
</body>
</html>`
