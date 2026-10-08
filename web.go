package main

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"hash/fnv"
	"html/template"
	"maps"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html static/*
var assets embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"usd": fmtUSD, "num": fmtNum, "k": fmtKAny, "pct": fmtPct, "pct2": fmtPct2, "ago": fmtAgo,
	"delta": fmtDelta, "price": fmtPrice,
	"ethprice": func(v float64) string { return "$" + fmtNum(int(math.Round(v))) },
	"inc":      func(i int) int { return i + 1 },
	"add":      func(a, b int) int { return a + b },
	"sub":      func(a, b int) int { return a - b },
	"icon":     iconHTML,
	"trend": func(p float64) string {
		switch {
		case p > 0:
			return "up"
		case p < 0:
			return "down"
		}
		return ""
	},
	"int":  func(v uint64) int { return int(v) },
	"host": func(u string) string { return strings.TrimPrefix(u, "https://") },
}).ParseFS(assets, "templates/*.html"))

// ---- formatting ----

func fmtUSD(v float64) string {
	switch a := math.Abs(v); {
	case a >= 1e9:
		return fmt.Sprintf("$%.1fB", v/1e9)
	case a >= 1e6:
		return fmt.Sprintf("$%.1fM", v/1e6)
	case a >= 1e3:
		return fmt.Sprintf("$%.1fK", v/1e3)
	case a == 0:
		return "$0"
	case a < 10:
		return fmt.Sprintf("$%.2f", v)
	default:
		return fmt.Sprintf("$%.0f", v)
	}
}

func fmtK(v float64) string {
	switch a := math.Abs(v); {
	case a >= 1e9:
		return fmt.Sprintf("%.1fB", v/1e9)
	case a >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case a >= 1e3:
		return fmt.Sprintf("%.1fK", v/1e3)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

func fmtKAny(v any) string {
	switch x := v.(type) {
	case int:
		return fmtK(float64(x))
	case float64:
		return fmtK(x)
	}
	return fmt.Sprint(v)
}

func fmtPrice(v float64) string {
	switch {
	case v == 0:
		return "–"
	case v >= 1:
		return fmt.Sprintf("$%.2f", v)
	case v >= 0.01:
		return fmt.Sprintf("$%.4f", v)
	default:
		return fmt.Sprintf("$%.6f", v)
	}
}

func fmtNum(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// fmtPct rounds to whole percent; a non-zero sliver reads "<1%", not "0%".
func fmtPct(v float64) string {
	if v > 0 && v < 0.5 {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", v)
}
func fmtPct2(v float64) string { return fmt.Sprintf("%.2f%%", v) }

func fmtAgo(ts int64) string {
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// fmtDelta renders a signed percentage change, uncapped, with thousands
// separators for big jumps ("+18,300%").
func fmtDelta(p float64) template.HTML {
	if p == 0 || math.IsInf(p, 0) || math.IsNaN(p) {
		return ""
	}
	cls, sign := "up", "+"
	if p < 0 {
		cls, sign = "down", "-"
	}
	return template.HTML(fmt.Sprintf(`<span class="%s">%s%s%%</span>`, cls, sign, fmtNum(int(math.Round(math.Abs(p))))))
}

func pctChange(cur, prev float64) float64 {
	if prev == 0 {
		return 0
	}
	return (cur - prev) / prev * 100
}

func (c *collector) routes() http.Handler {
	mux := http.NewServeMux()
	rc := &respCache{state: c.state}
	mux.Handle("/static/", rc.wrap(cacheStatic(http.FileServer(http.FS(assets)))))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		c.state.mu.RLock()
		head := c.state.Head
		c.state.mu.RUnlock()
		fmt.Fprintf(w, "ok head=%d\n", head)
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "User-agent: *\nAllow: /\nDisallow: /api/snapshot.json\nSitemap: %s/sitemap.xml\n", origin(r))
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		o := origin(r)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
		for _, p := range []string{"/", "/tokens", "/api"} {
			fmt.Fprintf(w, `<url><loc>%s%s</loc><changefreq>hourly</changefreq></url>`, o, p)
		}
		fmt.Fprint(w, `</urlset>`)
	})
	mux.HandleFunc("/llms.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		tmpl.ExecuteTemplate(w, "llms.txt", map[string]string{"Origin": origin(r)})
	})

	api := http.NewServeMux()
	api.HandleFunc("/api/snapshot.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.ServeFile(w, r, c.state.path)
	})
	api.HandleFunc("/api/v1/summary", c.apiSummary)
	api.HandleFunc("/api/v1/daily", c.apiDaily)
	api.HandleFunc("/api/v1/tokens", c.apiTokens)
	api.HandleFunc("/api/v1/tokens/{address}", c.apiToken)
	// The limiter sits outside the cache so hits still count and its headers stay per-client.
	mux.Handle("/api/", newRateLimit().wrap(rc.wrap(c.apiHeaders(api))))

	page := func(name string, build func(*http.Request) pageView) http.Handler {
		return rc.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			pv := build(r)
			pv.Origin = origin(r)
			pv.Path = r.URL.Path
			if err := tmpl.ExecuteTemplate(w, name, pv); err != nil {
				http.Error(w, err.Error(), 500)
			}
		}))
	}
	mux.Handle("/{$}", page("overview.html", func(*http.Request) pageView { return buildView(c.state) }))
	mux.Handle("/charts", http.RedirectHandler("/", http.StatusMovedPermanently))
	// One comparison chart (window × metric × totals/share) as an HTML fragment;
	// the overview renders its default inline and fetches the rest on demand.
	mux.Handle("/chart", rc.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		c.state.mu.RLock()
		cc := comparison(hoursByPad(c.state), time.Now().UTC(), q.Get("range"), q.Get("metric"), q.Get("mode") == "share")
		c.state.mu.RUnlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "cmpfrag", cc); err != nil {
			http.Error(w, err.Error(), 500)
		}
	})))
	mux.Handle("/tokens", page("tokens.html", func(r *http.Request) pageView { return buildTokens(c.state, r) }))
	mux.Handle("/api", page("api.html", func(r *http.Request) pageView {
		c.state.mu.RLock()
		defer c.state.mu.RUnlock()
		pv := envelope(c.state, hoursByPad(c.state))
		pv.Title, pv.Page = "API", "api"
		pv.Host = r.Host
		return pv
	}))
	return secure(mux)
}

// origin reconstructs the public origin, honouring a reverse proxy's scheme.
func origin(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" && strings.HasPrefix(r.Host, "127.0.0.1") {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// secure adds baseline security headers to every response.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src * data:; style-src 'self' 'unsafe-inline'; font-src 'self'; script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// cacheStatic lets browsers keep embedded assets for a day, or for good when
// the URL carries the content-hash version (?v=assetVersion).
func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cc := "public, max-age=86400"
		if r.URL.Query().Get("v") == assetVersion {
			cc = "public, max-age=31536000, immutable"
		}
		w.Header().Set("Cache-Control", cc)
		next.ServeHTTP(w, r)
	})
}

// respCache memoises GET responses per (sync, wall-clock minute, origin, URI)
// and serves them gzipped when accepted. A finished sync chunk invalidates at
// once; the minute key keeps "synced Xm ago", token ages and the today/24h
// windows as current as live rendering did. ETags are content hashes, so
// conditional requests get 304 whenever the bytes are unchanged.
// ponytail: cacheMax entries per epoch, then uncached; no singleflight, so
// concurrent misses on one URL each render once.
type respCache struct {
	state *State
	mu    sync.Mutex
	epoch [2]int64
	store map[string]*cachedResp
}

const cacheMax = 256

type cachedResp struct {
	header   http.Header
	body, gz []byte // gz is nil when compression does not pay
	etag     string
}

func (rc *respCache) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		rc.state.mu.RLock()
		epoch := [2]int64{rc.state.SyncedAt, time.Now().Unix() / 60}
		rc.state.mu.RUnlock()
		key := origin(r) + r.URL.RequestURI()
		rc.mu.Lock()
		if epoch != rc.epoch {
			rc.store, rc.epoch = map[string]*cachedResp{}, epoch
		}
		e := rc.store[key]
		rc.mu.Unlock()
		if e == nil {
			rec := &recorder{header: http.Header{}, code: http.StatusOK}
			get := r.Clone(r.Context())
			get.Method = http.MethodGet // HEAD shares the GET entry; net/http drops the body
			next.ServeHTTP(rec, get)
			if rec.code != http.StatusOK {
				maps.Copy(w.Header(), rec.header)
				w.WriteHeader(rec.code)
				w.Write(rec.buf)
				return
			}
			e = newCachedResp(rec)
			rc.mu.Lock()
			if rc.epoch == epoch && len(rc.store) < cacheMax {
				rc.store[key] = e
			}
			rc.mu.Unlock()
		}
		e.serve(w, r)
	})
}

func newCachedResp(rec *recorder) *cachedResp {
	h := rec.header
	h.Del("Content-Length") // recomputed per encoding
	h.Del("Accept-Ranges")  // cached bodies are served whole
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", http.DetectContentType(rec.buf))
	}
	sum := fnv.New64a()
	sum.Write(rec.buf)
	e := &cachedResp{header: h, body: rec.buf, etag: fmt.Sprintf(`W/"%x"`, sum.Sum64())}
	var gz bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	zw.Write(rec.buf)
	zw.Close()
	if gz.Len() < len(rec.buf)*9/10 {
		e.gz = gz.Bytes()
	}
	return e
}

func (e *cachedResp) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	maps.Copy(h, e.header)
	h.Set("ETag", e.etag)
	h.Set("Vary", "Accept-Encoding")
	if inm := r.Header.Get("If-None-Match"); inm != "" && strings.Contains(inm, e.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := e.body
	if e.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = e.gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

// recorder captures a handler's response for the cache.
type recorder struct {
	header http.Header
	buf    []byte
	code   int
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(code int)        { r.code = code }
func (r *recorder) Write(b []byte) (int, error) { r.buf = append(r.buf, b...); return len(b), nil }

func logoOf(q *Quote) string {
	if q == nil || q.Logo == "-" {
		return ""
	}
	return q.Logo
}

// iconHTML renders a small round icon, falling back to the first letters of
// the label when no image is known. Broken remote images swap to the fallback.
func iconHTML(src, label string) template.HTML {
	initials := strings.ToUpper(label)
	if len(initials) > 2 {
		initials = initials[:2]
	}
	fb := fmt.Sprintf(`<i class="ico fb" title="%s">%s</i>`, template.HTMLEscapeString(label), template.HTMLEscapeString(initials))
	if src == "" || src == "-" {
		return template.HTML(fb)
	}
	return template.HTML(fmt.Sprintf(`<img class="ico" src="%s" alt="%s" loading="lazy" referrerpolicy="no-referrer" onerror="this.outerHTML=this.nextElementSibling.innerHTML"><template>%s</template>`,
		template.HTMLEscapeString(src), template.HTMLEscapeString(label), fb))
}

func fnv32(b []byte) uint32 {
	h := fnv.New32a()
	h.Write(b)
	return h.Sum32()
}
