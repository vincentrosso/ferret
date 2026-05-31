package report

import (
	"html/template"
	"os"
	"path/filepath"
	"time"
)

// DetailPage holds all data for a single-lot deep-dive page.
type DetailPage struct {
	Lot
	VIN             string
	EngineType      string
	Transmission    string
	Color           string
	BodyStyle       string
	DriveType       string
	FuelType        string
	RunAndDrive     string
	KeysPresent     string
	AirbagsDeployed string
	ConditionGrade  string
	LossType        string
	SaleDate        string
	ExteriorPanels  []PanelItem
	GalleryImages   []template.URL
	Generated       string
	ReportPath      string // relative link back to parent report
}

// PanelItem is one row from the exterior condition assessment.
type PanelItem struct {
	Panel  string
	Damage string
	Count  string
}

// GenerateDetail writes a single-lot deep-dive HTML page to outPath.
func GenerateDetail(dp DetailPage, outPath string) error {
	dp.Generated = time.Now().Format("Jan 2, 2006 3:04 PM")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return detailTmpl.Execute(f, dp)
}

var detailTmpl = template.Must(template.New("detail").Funcs(funcMap).Parse(detailTemplate))

const detailTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.Year}} {{.Make}} {{.Model}} — Lot #{{.LotNumber}}</title>
<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #0f172a; color: #e2e8f0; min-height: 100vh; }
a { color: #3b82f6; text-decoration: none; }
a:hover { text-decoration: underline; }

.topbar { background: #1e293b; border-bottom: 1px solid #334155; padding: 12px 20px; display: flex; align-items: center; gap: 14px; flex-wrap: wrap; position: sticky; top: 0; z-index: 10; }
.back { font-size: 0.78rem; color: #94a3b8; white-space: nowrap; }
.back:hover { color: #e2e8f0; text-decoration: none; }
.topbar-title { flex: 1; }
.topbar-title h1 { font-size: 1.1rem; font-weight: 700; color: #f8fafc; line-height: 1.2; }
.topbar-title .sub { font-size: 0.7rem; color: #64748b; margin-top: 2px; }
.sev-pill { display: inline-block; padding: 3px 10px; border-radius: 99px; font-size: 0.68rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.05em; flex-shrink: 0; }
.copart-btn { background: #1d4ed8; border-radius: 7px; padding: 6px 12px; font-size: 0.75rem; font-weight: 600; color: #fff; white-space: nowrap; }
.copart-btn:hover { background: #2563eb; text-decoration: none; }

/* gallery */
.gallery-wrap { background: #000; }
.gallery { display: flex; overflow-x: auto; gap: 6px; padding: 8px; scroll-behavior: smooth; scrollbar-width: thin; scrollbar-color: #334155 #000; }
.gallery::-webkit-scrollbar { height: 5px; }
.gallery::-webkit-scrollbar-track { background: #000; }
.gallery::-webkit-scrollbar-thumb { background: #334155; border-radius: 3px; }
.gallery img { height: 260px; width: auto; flex-shrink: 0; object-fit: cover; border-radius: 5px; cursor: zoom-in; opacity: 0.95; transition: opacity 0.1s; }
.gallery img:hover { opacity: 1; }
.no-gallery { padding: 40px; text-align: center; color: #475569; font-size: 0.8rem; }

/* lightbox */
.lb { display: none; position: fixed; inset: 0; background: rgba(0,0,0,0.93); z-index: 999; flex-direction: column; align-items: center; justify-content: center; }
.lb.open { display: flex; }
.lb-img { max-width: 94vw; max-height: 88vh; object-fit: contain; border-radius: 4px; }
.lb-close { position: absolute; top: 14px; right: 18px; font-size: 2rem; color: #94a3b8; cursor: pointer; line-height: 1; background: none; border: none; }
.lb-close:hover { color: #fff; }
.lb-prev, .lb-next { position: absolute; top: 50%; transform: translateY(-50%); background: rgba(255,255,255,0.08); border: none; color: #e2e8f0; font-size: 1.8rem; padding: 14px 12px; cursor: pointer; border-radius: 6px; }
.lb-prev:hover, .lb-next:hover { background: rgba(255,255,255,0.15); }
.lb-prev { left: 10px; }
.lb-next { right: 10px; }
.lb-counter { position: absolute; bottom: 16px; font-size: 0.72rem; color: #64748b; }

/* body */
.body { padding: 18px 20px 32px; max-width: 1100px; margin: 0 auto; }

/* decision */
.decision { background: #1e293b; border: 1px solid #334155; border-radius: 10px; padding: 16px 18px; margin-bottom: 18px; }
.section-label { font-size: 0.6rem; text-transform: uppercase; letter-spacing: 0.09em; color: #475569; font-weight: 700; margin-bottom: 12px; }
.dec-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); gap: 12px 18px; }
.dec-item { display: flex; flex-direction: column; gap: 2px; }
.dec-label { font-size: 0.58rem; text-transform: uppercase; letter-spacing: 0.05em; color: #64748b; }
.dec-val { font-size: 1.05rem; font-weight: 700; }
.c-slate { color: #94a3b8; }
.c-blue  { color: #60a5fa; }
.c-green { color: #4ade80; }
.c-amber { color: #fbbf24; }
.c-red   { color: #f87171; }

/* 2-col grid */
.row2 { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; margin-bottom: 14px; }
@media (max-width: 680px) { .row2 { grid-template-columns: 1fr; } }

.card { background: #1e293b; border: 1px solid #334155; border-radius: 10px; padding: 15px 16px; }

/* spec table */
.spec { width: 100%; border-collapse: collapse; }
.spec td { padding: 4px 0; vertical-align: top; }
.spec td:first-child { font-size: 0.6rem; text-transform: uppercase; letter-spacing: 0.04em; color: #64748b; width: 42%; padding-right: 10px; padding-top: 5px; }
.spec td:last-child { font-size: 0.82rem; font-weight: 500; color: #e2e8f0; }
.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 0.74rem !important; letter-spacing: 0.03em; }

/* flags */
.flags { display: flex; flex-wrap: wrap; gap: 7px; margin-top: 12px; }
.flag { padding: 3px 9px; border-radius: 5px; font-size: 0.68rem; font-weight: 600; }
.flag-ok  { background: #14532d; color: #4ade80; }
.flag-bad { background: #450a0a; color: #f87171; }
.flag-warn { background: #451a03; color: #fb923c; }
.flag-na  { background: #0f172a; color: #64748b; border: 1px solid #334155; }

/* panels */
.panel-chips { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 8px; }
.chip { background: #0f172a; border: 1px solid #334155; border-radius: 5px; padding: 2px 8px; font-size: 0.65rem; color: #94a3b8; }

/* exterior table */
.ext { width: 100%; font-size: 0.73rem; border-collapse: collapse; margin-top: 10px; }
.ext th { text-align: left; padding: 5px 8px; font-size: 0.58rem; text-transform: uppercase; letter-spacing: 0.05em; color: #475569; border-bottom: 1px solid #334155; }
.ext td { padding: 5px 8px; border-bottom: 1px solid #1a2844; color: #cbd5e1; }
.ext tr:last-child td { border-bottom: none; }

/* score bars */
.bar-row { display: flex; align-items: center; gap: 8px; margin-bottom: 7px; }
.bar-lbl { width: 68px; font-size: 0.62rem; color: #64748b; flex-shrink: 0; }
.bar-track { flex: 1; background: #0f172a; border-radius: 3px; height: 6px; overflow: hidden; }
.bar-fill { height: 100%; border-radius: 3px; background: #3b82f6; }
.bar-val { width: 48px; font-size: 0.68rem; font-weight: 600; color: #e2e8f0; text-align: right; flex-shrink: 0; }
.bar-max { font-size: 0.58rem; font-weight: 400; color: #475569; }
.score-total { float: right; font-size: 1rem; font-weight: 700; color: #60a5fa; }

/* research buttons */
.btn-row { display: flex; flex-wrap: wrap; gap: 8px; }
.btn { display: inline-flex; align-items: center; padding: 7px 13px; border-radius: 7px; font-size: 0.75rem; font-weight: 600; border: 1px solid #334155; background: #0f172a; color: #e2e8f0; transition: border-color 0.12s; }
.btn:hover { border-color: #3b82f6; text-decoration: none; }
.btn-primary { background: #1d4ed8; border-color: #2563eb; color: #fff; }
.btn-primary:hover { background: #2563eb; }

.footer { text-align: center; font-size: 0.62rem; color: #334155; padding: 8px 0 16px; }
</style>
</head>
<body>

<div class="topbar">
  <a class="back" href="{{.ReportPath}}">← Report</a>
  <div class="topbar-title">
    <h1>{{.Year}} {{.Make}} {{.Model}}{{if .BodyStyle}} · {{.BodyStyle}}{{end}}</h1>
    <div class="sub">Lot #{{.LotNumber}}{{if .SaleDate}} · Auction {{.SaleDate}}{{end}}{{if .YardName}} · {{.YardName}}{{end}}</div>
  </div>
  {{- if .Damage}}
  <span class="sev-pill" style="background:{{severityBg .Damage.Severity}};color:{{severityColor .Damage.Severity}}">{{.Damage.Severity}}</span>
  {{- end}}
  <a class="copart-btn" href="{{.LotURL | safeURL}}" target="_blank">Copart ↗</a>
</div>

{{- if .GalleryImages}}
<div class="gallery-wrap">
  <div class="gallery" id="gallery">
    {{- range $i, $img := .GalleryImages}}
    <img src="{{$img}}" loading="{{if lt $i 4}}eager{{else}}lazy{{end}}" onclick="openLB({{$i}})">
    {{- end}}
  </div>
</div>
<div class="lb" id="lb" onclick="lbBg(event)">
  <button class="lb-close" onclick="closeLB()">×</button>
  <button class="lb-prev" onclick="lbNav(-1)">‹</button>
  <img class="lb-img" id="lb-img" src="">
  <button class="lb-next" onclick="lbNav(1)">›</button>
  <span class="lb-counter" id="lb-counter"></span>
</div>
{{- else}}
<div class="no-gallery">No images available</div>
{{- end}}

<div class="body">

  <div class="decision">
    <div class="section-label">Bid Decision</div>
    <div class="dec-grid">
      <div class="dec-item">
        <span class="dec-label">Current Bid</span>
        <span class="dec-val c-slate">{{dollar (ftoi .CurrentBid)}}</span>
      </div>
      {{- if .Damage}}
      <div class="dec-item">
        <span class="dec-label">Repair Estimate</span>
        <span class="dec-val c-amber">{{dollar .Damage.RepairCostLow}}–{{dollar .Damage.RepairCostHigh}}</span>
      </div>
      {{- end}}
      <div class="dec-item">
        <span class="dec-label">Est. Resale</span>
        <span class="dec-val c-blue">{{dollar .EstResale}}</span>
      </div>
      <div class="dec-item">
        <span class="dec-label">Max Bid (50% ROI)</span>
        <span class="dec-val c-green">{{dollar .MaxBid}}</span>
      </div>
      {{- if .Damage}}
      <div class="dec-item">
        <span class="dec-label">Total In (bid+repair)</span>
        <span class="dec-val c-slate">{{dollar .TotalCostLow}}–{{dollar .TotalCostHigh}}</span>
      </div>
      {{- end}}
      <div class="dec-item">
        <span class="dec-label">ROI @ 80% Max Bid</span>
        <span class="dec-val {{if ge .ROIAt80Pct 40}}c-green{{else if ge .ROIAt80Pct 20}}c-amber{{else}}c-red{{end}}">{{.ROIAt80Pct}}%</span>
      </div>
      <div class="dec-item">
        <span class="dec-label">Score</span>
        <span class="dec-val c-blue">{{.Score.Total}}/100</span>
      </div>
    </div>
  </div>

  <div class="row2">
    <div class="card">
      <div class="section-label">Vehicle Details</div>
      <table class="spec">
        {{- if .VIN}}<tr><td>VIN</td><td class="mono">{{.VIN}}</td></tr>{{end}}
        <tr><td>Odometer</td><td>{{commas .Odometer}} mi</td></tr>
        {{- if .EngineType}}<tr><td>Engine</td><td>{{.EngineType}}</td></tr>{{end}}
        {{- if .Transmission}}<tr><td>Transmission</td><td>{{.Transmission}}</td></tr>{{end}}
        {{- if .FuelType}}<tr><td>Fuel</td><td>{{.FuelType}}</td></tr>{{end}}
        {{- if .DriveType}}<tr><td>Drive</td><td>{{.DriveType}}</td></tr>{{end}}
        {{- if .Color}}<tr><td>Color</td><td>{{.Color}}</td></tr>{{end}}
        {{- if .BodyStyle}}<tr><td>Body</td><td>{{.BodyStyle}}</td></tr>{{end}}
        {{- if .LossType}}<tr><td>Loss Type</td><td>{{.LossType}}</td></tr>{{end}}
        {{- if .ConditionGrade}}<tr><td>Grade</td><td>{{.ConditionGrade}}</td></tr>{{end}}
        <tr><td>Title</td><td>{{.TitleType}}</td></tr>
      </table>
      <div class="flags">
        {{- if .RunAndDrive}}
        <span class="flag {{if eq .RunAndDrive "Yes"}}flag-ok{{else}}flag-bad{{end}}">R&amp;D: {{.RunAndDrive}}</span>
        {{- end}}
        {{- if .KeysPresent}}
        <span class="flag {{if eq .KeysPresent "Yes"}}flag-ok{{else}}flag-bad{{end}}">Keys: {{.KeysPresent}}</span>
        {{- end}}
        {{- if .AirbagsDeployed}}
        <span class="flag {{if eq .AirbagsDeployed "Not Deployed"}}flag-ok{{else}}flag-bad{{end}}">Airbags: {{.AirbagsDeployed}}</span>
        {{- end}}
      </div>
    </div>

    <div class="card">
      <div class="section-label">Damage Analysis</div>
      {{- if .Damage}}
      <table class="spec">
        <tr><td>Severity</td><td><span class="sev-pill" style="background:{{severityBg .Damage.Severity}};color:{{severityColor .Damage.Severity}}">{{.Damage.Severity}}</span></td></tr>
        <tr><td>PDR Viable</td><td>{{if .Damage.PDRViable}}✓ Yes{{else}}✗ No{{end}}</td></tr>
        <tr><td>Dent Count</td><td>~{{.Damage.DentCountEst}}</td></tr>
        <tr><td>Confidence</td><td>{{.Damage.Confidence}}</td></tr>
        <tr><td>Images Used</td><td>{{.Damage.ImagesAnalyzed}}</td></tr>
      </table>
      {{- if .Damage.AffectedPanels}}
      <div style="margin-top:12px">
        <div style="font-size:0.58rem;text-transform:uppercase;letter-spacing:0.05em;color:#475569;margin-bottom:5px">Affected Panels</div>
        <div class="panel-chips">{{- range .Damage.AffectedPanels}}<span class="chip">{{.}}</span>{{end}}</div>
      </div>
      {{- end}}
      {{- if .Damage.Notes}}
      <div style="margin-top:12px;font-size:0.73rem;color:#94a3b8;line-height:1.55;border-top:1px solid #334155;padding-top:10px">{{.Damage.Notes}}</div>
      {{- end}}
      {{- else}}
      <div style="color:#475569;font-size:0.8rem">No damage analysis available.</div>
      {{- end}}

      {{- if .ExteriorPanels}}
      <div style="margin-top:16px">
        <div class="section-label">Exterior Condition</div>
        <table class="ext">
          <thead><tr><th>Panel</th><th>Damage</th><th>Count</th></tr></thead>
          <tbody>
            {{- range .ExteriorPanels}}
            <tr><td>{{.Panel}}</td><td>{{.Damage}}</td><td>{{.Count}}</td></tr>
            {{- end}}
          </tbody>
        </table>
      </div>
      {{- end}}
    </div>
  </div>

  <div class="row2">
    <div class="card">
      <div class="section-label">Score Breakdown <span class="score-total">{{.Score.Total}}/100</span></div>
      <div class="bar-row"><span class="bar-lbl">Damage</span><div class="bar-track"><div class="bar-fill" style="width:{{pct .Score.Damage 30}}%"></div></div><span class="bar-val">{{.Score.Damage}}<span class="bar-max">/30</span></span></div>
      <div class="bar-row"><span class="bar-lbl">Make</span><div class="bar-track"><div class="bar-fill" style="width:{{pct .Score.Make 20}}%"></div></div><span class="bar-val">{{.Score.Make}}<span class="bar-max">/20</span></span></div>
      <div class="bar-row"><span class="bar-lbl">Mileage</span><div class="bar-track"><div class="bar-fill" style="width:{{pct .Score.Mileage 20}}%"></div></div><span class="bar-val">{{.Score.Mileage}}<span class="bar-max">/20</span></span></div>
      <div class="bar-row"><span class="bar-lbl">Year</span><div class="bar-track"><div class="bar-fill" style="width:{{pct .Score.Year 15}}%"></div></div><span class="bar-val">{{.Score.Year}}<span class="bar-max">/15</span></span></div>
      <div class="bar-row"><span class="bar-lbl">Bid</span><div class="bar-track"><div class="bar-fill" style="width:{{pct .Score.Bid 10}}%"></div></div><span class="bar-val">{{.Score.Bid}}<span class="bar-max">/10</span></span></div>
      <div class="bar-row"><span class="bar-lbl">Model Tier</span><div class="bar-track"><div class="bar-fill" style="width:{{pct .Score.ModelTier 5}}%"></div></div><span class="bar-val">{{.Score.ModelTier}}<span class="bar-max">/5</span></span></div>
    </div>

    <div class="card">
      <div class="section-label">Research</div>
      <div class="btn-row">
        <a class="btn btn-primary" href="{{.LotURL | safeURL}}" target="_blank">Copart Lot ↗</a>
        {{- if .VIN}}
        <a class="btn" href="https://www.carfax.com/vehicle/{{.VIN}}" target="_blank">Carfax ↗</a>
        <a class="btn" href="https://www.autocheck.com/vehiclehistory/autocheck/en/vehiclehistory?vin={{.VIN}}" target="_blank">AutoCheck ↗</a>
        {{- end}}
      </div>
    </div>
  </div>

</div>

<div class="footer">Generated {{.Generated}}</div>

<script>
var lbIdx = 0;
var lbImgs = null;
function getLbImgs() { if (!lbImgs) lbImgs = Array.from(document.querySelectorAll('#gallery img')); return lbImgs; }
function openLB(i) {
  var imgs = getLbImgs();
  lbIdx = i;
  document.getElementById('lb-img').src = imgs[i].src;
  document.getElementById('lb-counter').textContent = (i+1) + ' / ' + imgs.length;
  document.getElementById('lb').classList.add('open');
}
function closeLB() { document.getElementById('lb').classList.remove('open'); }
function lbBg(e) { if (e.target === document.getElementById('lb')) closeLB(); }
function lbNav(d) {
  var imgs = getLbImgs();
  lbIdx = (lbIdx + d + imgs.length) % imgs.length;
  document.getElementById('lb-img').src = imgs[lbIdx].src;
  document.getElementById('lb-counter').textContent = (lbIdx+1) + ' / ' + imgs.length;
}
document.addEventListener('keydown', function(e) {
  if (!document.getElementById('lb').classList.contains('open')) return;
  if (e.key === 'ArrowRight') lbNav(1);
  else if (e.key === 'ArrowLeft') lbNav(-1);
  else if (e.key === 'Escape') closeLB();
});
</script>
</body>
</html>`
