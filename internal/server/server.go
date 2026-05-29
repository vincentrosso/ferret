package server

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

type Server struct {
	store   *MemStore
	dataDir string
	tmpl    *template.Template
}

func New(dataDir string) (*Server, error) {
	store := NewMemStore(dataDir)
	n, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("load store: %w", err)
	}
	slog.Info("store loaded", "lots", n)

	tmpl, err := template.New("").Funcs(template.FuncMap{
		"add":   func(a, b int) int { return a + b },
		"gt0":   func(n int) bool { return n > 0 },
		"seq":   func(n int) []int { s := make([]int, n); for i := range s { s[i] = i }; return s },
		"comma": func(n int) string { return strconv.Itoa(n) }, // TODO: real comma formatting
		"formatBid": func(f float64) string {
			if f == 0 {
				return "—"
			}
			return fmt.Sprintf("$%,.0f", f)
		},
		"hasZip": func(s string) bool { return s != "" },
	}).Parse(htmlTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	return &Server{store: store, dataDir: dataDir, tmpl: tmpl}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /lot/{id}", s.handleLot)
	mux.HandleFunc("GET /images/{lot}/{index}", s.handleImage)
	mux.HandleFunc("POST /api/reload", s.handleReload)
	return mux
}

// handleIndex renders the lots list with optional ?q= filter.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("q"))
	all := s.store.All()

	var filtered []*LotRecord
	for _, l := range all {
		if q == "" {
			filtered = append(filtered, l)
			continue
		}
		haystack := strings.ToLower(l.Make + " " + l.Model + " " + l.LotNumber +
			" " + l.DamagePrimary + " " + l.TitleType + " " + l.Color)
		if strings.Contains(haystack, q) {
			filtered = append(filtered, l)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.tmpl.ExecuteTemplate(w, "index", map[string]any{
		"Lots":  filtered,
		"Total": s.store.Count(),
		"Query": q,
	})
}

// handleLot renders a single lot detail page.
func (s *Server) handleLot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	lot, ok := s.store.Get(id)
	if !ok {
		http.Error(w, "lot not found", http.StatusNotFound)
		return
	}

	// Count images in ZIP
	imageCount := 0
	if lot.ImageZip != "" {
		if zr, err := zip.OpenReader(lot.ImageZip); err == nil {
			imageCount = len(zr.File)
			zr.Close()
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.tmpl.ExecuteTemplate(w, "lot", map[string]any{
		"Lot":        lot,
		"ImageCount": imageCount,
	})
}

// handleImage streams image N from the lot's ZIP file.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	lotNum := r.PathValue("lot")
	idxStr := r.PathValue("index")
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 0 {
		http.Error(w, "bad index", http.StatusBadRequest)
		return
	}

	zipPath := filepath.Join(s.dataDir, "images", lotNum+".zip")
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		http.Error(w, "zip not found", http.StatusNotFound)
		return
	}
	defer zr.Close()

	if idx >= len(zr.File) {
		http.Error(w, "index out of range", http.StatusNotFound)
		return
	}

	f := zr.File[idx]
	rc, err := f.Open()
	if err != nil {
		http.Error(w, "open zip entry", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	ext := strings.ToLower(filepath.Ext(f.Name))
	switch ext {
	case ".jpg", ".jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".webp":
		w.Header().Set("Content-Type", "image/webp")
	default:
		w.Header().Set("Content-Type", "image/jpeg")
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	io.Copy(w, rc)
}

// handleReload reloads the store from disk and returns updated count as JSON.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("store reloaded", "lots", n)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"lots": n})
}

// ── HTML templates ────────────────────────────────────────────────────────

const navHTML = `
<nav class="bg-white border-b border-gray-200 px-6 py-3 flex items-center gap-6">
  <a href="/" class="text-lg font-bold tracking-tight text-gray-900">🐾 Ferret</a>
  <span class="text-sm text-gray-400">Copart Hail Arb</span>
  <div class="ml-auto">
    <button hx-post="/api/reload" hx-swap="none" hx-on::after-request="window.location.reload()"
      class="text-sm px-3 py-1 rounded border border-gray-300 hover:bg-gray-100 transition">
      ↺ Reload
    </button>
  </div>
</nav>`

const headHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Ferret</title>
  <script src="https://cdn.tailwindcss.com"></script>
  <script src="https://unpkg.com/htmx.org@1.9.12"></script>
</head>
<body class="bg-gray-50 text-gray-900 min-h-screen">`

const htmlTemplate = headHTML + navHTML + `

{{define "index"}}
` + headHTML + navHTML + `
<div class="max-w-7xl mx-auto px-4 py-6">
  <div class="flex items-center gap-4 mb-5">
    <span class="text-sm text-gray-500">{{.Total}} lots in store</span>
    <form class="flex-1 max-w-sm" method="GET" action="/">
      <input name="q" value="{{.Query}}" placeholder="Filter by make, model, damage, lot#…"
        class="w-full border border-gray-300 rounded-md px-3 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
    </form>
    {{if .Query}}<a href="/" class="text-sm text-blue-600 hover:underline">Clear</a>{{end}}
    <span class="text-sm text-gray-400">{{len .Lots}} shown</span>
  </div>

  <div class="bg-white rounded-lg shadow overflow-x-auto">
    <table class="min-w-full text-sm divide-y divide-gray-200">
      <thead class="bg-gray-50 sticky top-0">
        <tr>
          <th class="px-4 py-2.5 text-left text-xs font-medium text-gray-500 uppercase">Lot</th>
          <th class="px-4 py-2.5 text-left text-xs font-medium text-gray-500 uppercase">Vehicle</th>
          <th class="px-4 py-2.5 text-left text-xs font-medium text-gray-500 uppercase">Odometer</th>
          <th class="px-4 py-2.5 text-left text-xs font-medium text-gray-500 uppercase">Damage</th>
          <th class="px-4 py-2.5 text-left text-xs font-medium text-gray-500 uppercase">Title</th>
          <th class="px-4 py-2.5 text-left text-xs font-medium text-gray-500 uppercase">Bid</th>
          <th class="px-4 py-2.5 text-left text-xs font-medium text-gray-500 uppercase">Auction</th>
          <th class="px-4 py-2.5 text-center text-xs font-medium text-gray-500 uppercase">R&amp;D</th>
          <th class="px-4 py-2.5 text-center text-xs font-medium text-gray-500 uppercase">Imgs</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-gray-100">
        {{range .Lots}}
        <tr class="hover:bg-blue-50 cursor-pointer" onclick="window.location='/lot/{{.LotNumber}}'">
          <td class="px-4 py-2.5 font-mono text-xs text-gray-400">{{.LotNumber}}</td>
          <td class="px-4 py-2.5">
            <div class="font-semibold text-gray-900">{{.Year}} {{.Make}} {{.Model}}</div>
            {{if .Color}}<div class="text-xs text-gray-400">{{.Color}}{{if .BodyStyle}} · {{.BodyStyle}}{{end}}</div>{{end}}
          </td>
          <td class="px-4 py-2.5 text-gray-700">{{if gt0 .Odometer}}{{comma .Odometer}} mi{{else}}—{{end}}</td>
          <td class="px-4 py-2.5">
            {{if .DamagePrimary}}<span class="inline-block bg-blue-100 text-blue-700 text-xs px-2 py-0.5 rounded">{{.DamagePrimary}}</span>{{end}}
          </td>
          <td class="px-4 py-2.5">
            {{if .TitleType}}
              {{if eq .TitleType "SV"}}
                <span class="inline-block bg-red-100 text-red-700 text-xs px-2 py-0.5 rounded">Salvage</span>
              {{else}}
                <span class="inline-block bg-green-100 text-green-700 text-xs px-2 py-0.5 rounded">{{.TitleType}}</span>
              {{end}}
            {{end}}
          </td>
          <td class="px-4 py-2.5 font-semibold text-gray-800">{{formatBid .CurrentBid}}</td>
          <td class="px-4 py-2.5 text-xs text-gray-500">{{if .SaleDate}}{{.SaleDate}}{{else}}—{{end}}</td>
          <td class="px-4 py-2.5 text-center text-base">{{if eq .RunAndDrive "Yes"}}✅{{else if eq .RunAndDrive "No"}}❌{{else}}·{{end}}</td>
          <td class="px-4 py-2.5 text-center text-base">{{if hasZip .ImageZip}}📦{{else}}·{{end}}</td>
        </tr>
        {{else}}
        <tr><td colspan="9" class="px-4 py-12 text-center text-gray-400">No lots found.</td></tr>
        {{end}}
      </tbody>
    </table>
  </div>
</div>
</body></html>
{{end}}

{{define "lot"}}
{{$lot := .Lot}}
{{$n := .ImageCount}}
` + headHTML + navHTML + `
<div class="max-w-6xl mx-auto px-4 py-6">
  <div class="flex items-center gap-3 mb-5 text-sm">
    <a href="/" class="text-blue-600 hover:underline">← All lots</a>
    <span class="text-gray-300">|</span>
    <a href="{{$lot.LotURL}}" target="_blank" rel="noopener" class="text-blue-600 hover:underline">Copart ↗</a>
  </div>

  <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">

    <div class="lg:col-span-2 space-y-3">
      <div class="bg-white rounded-lg shadow overflow-hidden">
        {{if gt $n 0}}
        <div class="bg-black aspect-video flex items-center justify-center">
          <img id="main-img" src="/images/{{$lot.LotNumber}}/0"
               class="max-h-full max-w-full object-contain">
        </div>
        {{if gt $n 1}}
        <div class="p-3 flex gap-2 overflow-x-auto bg-gray-50">
          {{range $i := seq $n}}
          <img src="/images/{{$lot.LotNumber}}/{{$i}}"
               class="h-16 w-24 object-cover rounded cursor-pointer border-2 border-transparent hover:border-blue-400 flex-shrink-0"
               onclick="document.getElementById('main-img').src='/images/{{$lot.LotNumber}}/{{$i}}'">
          {{end}}
        </div>
        {{end}}
        {{else}}
        <div class="aspect-video bg-gray-100 flex items-center justify-center text-gray-400 text-sm">No images</div>
        {{end}}
      </div>
    </div>

    <div class="space-y-4">
      <div class="bg-white rounded-lg shadow p-4">
        <h1 class="text-xl font-bold">{{$lot.Year}} {{$lot.Make}} {{$lot.Model}}</h1>
        <div class="text-xs text-gray-400 mb-3">Lot #{{$lot.LotNumber}}</div>
        {{if $lot.CurrentBid}}
        <div class="text-3xl font-bold text-green-700">{{formatBid $lot.CurrentBid}}</div>
        <div class="text-xs text-gray-400 mb-3">Current bid</div>
        {{end}}
        <dl class="grid grid-cols-2 gap-y-2 text-sm mt-3">
          {{if $lot.Odometer}}<dt class="text-gray-500">Odometer</dt><dd class="font-medium">{{comma $lot.Odometer}} mi</dd>{{end}}
          {{if $lot.Color}}<dt class="text-gray-500">Color</dt><dd>{{$lot.Color}}</dd>{{end}}
          {{if $lot.BodyStyle}}<dt class="text-gray-500">Body</dt><dd>{{$lot.BodyStyle}}</dd>{{end}}
          {{if $lot.EngineType}}<dt class="text-gray-500">Engine</dt><dd>{{$lot.EngineType}}</dd>{{end}}
          {{if $lot.Transmission}}<dt class="text-gray-500">Trans</dt><dd>{{$lot.Transmission}}</dd>{{end}}
          {{if $lot.DriveType}}<dt class="text-gray-500">Drive</dt><dd>{{$lot.DriveType}}</dd>{{end}}
          {{if $lot.FuelType}}<dt class="text-gray-500">Fuel</dt><dd>{{$lot.FuelType}}</dd>{{end}}
          {{if $lot.VIN}}<dt class="text-gray-500">VIN</dt><dd class="font-mono text-xs break-all">{{$lot.VIN}}</dd>{{end}}
        </dl>
      </div>

      <div class="bg-white rounded-lg shadow p-4">
        <h2 class="text-xs font-semibold text-gray-500 uppercase tracking-wide mb-3">Condition</h2>
        <dl class="grid grid-cols-2 gap-y-2 text-sm">
          {{if $lot.DamagePrimary}}<dt class="text-gray-500">Primary</dt><dd><span class="bg-blue-100 text-blue-700 text-xs px-2 py-0.5 rounded">{{$lot.DamagePrimary}}</span></dd>{{end}}
          {{if $lot.DamageSecondary}}<dt class="text-gray-500">Secondary</dt><dd class="text-xs">{{$lot.DamageSecondary}}</dd>{{end}}
          {{if $lot.TitleType}}<dt class="text-gray-500">Title</dt>
            <dd>{{if eq $lot.TitleType "SV"}}<span class="bg-red-100 text-red-700 text-xs px-2 py-0.5 rounded">Salvage</span>{{else}}<span class="bg-green-100 text-green-700 text-xs px-2 py-0.5 rounded">{{$lot.TitleType}}</span>{{end}}</dd>{{end}}
          {{if $lot.RunAndDrive}}<dt class="text-gray-500">Run &amp; Drive</dt><dd>{{$lot.RunAndDrive}}</dd>{{end}}
          {{if $lot.KeysPresent}}<dt class="text-gray-500">Keys</dt><dd>{{$lot.KeysPresent}}</dd>{{end}}
          {{if $lot.AirbagsDeployed}}<dt class="text-gray-500">Airbags</dt><dd>{{$lot.AirbagsDeployed}}</dd>{{end}}
          {{if $lot.ConditionGrade}}<dt class="text-gray-500">Grade</dt><dd>{{$lot.ConditionGrade}}</dd>{{end}}
          {{if $lot.LossType}}<dt class="text-gray-500">Loss Type</dt><dd>{{$lot.LossType}}</dd>{{end}}
          {{if $lot.SaleDate}}<dt class="text-gray-500">Auction</dt><dd>{{$lot.SaleDate}}</dd>{{end}}
          {{if $lot.YardName}}<dt class="text-gray-500">Location</dt><dd>{{$lot.YardName}}</dd>{{end}}
        </dl>
        {{if $lot.ExteriorCondition}}
        <div class="mt-4 pt-3 border-t border-gray-100">
          <div class="text-xs font-semibold text-gray-500 uppercase mb-2">Exterior Damage</div>
          <div class="space-y-1">
            {{range $lot.ExteriorCondition}}
            <div class="flex text-xs gap-2">
              <span class="text-gray-400 w-28 shrink-0">{{.Panel}}</span>
              <span class="text-gray-700">{{.Damage}}</span>
              <span class="text-gray-400 ml-auto">({{.Count}})</span>
            </div>
            {{end}}
          </div>
        </div>
        {{end}}
      </div>
    </div>
  </div>
</div>
</body></html>
{{end}}
`
