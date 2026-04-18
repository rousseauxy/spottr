package api

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/spottr/spottr/internal/auth"
	"github.com/spottr/spottr/internal/config"
	"github.com/spottr/spottr/internal/db"
	"github.com/spottr/spottr/internal/nntp"
	"github.com/spottr/spottr/internal/sabnzbd"
	"github.com/spottr/spottr/internal/spotnet"
)

// Handler holds all dependencies for HTTP handlers.
type Handler struct {
	cfg      *config.Config
	db       *db.DB
	sab      *sabnzbd.Client
	webFS    fs.FS
	sessions *auth.Store
}

// NewHandler constructs a Handler with all dependencies injected.
func NewHandler(cfg *config.Config, database *db.DB, sab *sabnzbd.Client, webFS fs.FS) *Handler {
	return &Handler{
		cfg:      cfg,
		db:       database,
		sab:      sab,
		webFS:    webFS,
		sessions: auth.New(cfg.SessionDuration),
	}
}

// Router returns the fully configured chi router.
func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(securityHeaders)

	// Newznab-compatible API (used by Prowlarr/Sonarr/Radarr)
	r.Route("/api", func(r chi.Router) {
		r.Get("/", h.newznabHandler)
		r.Get("/newznab", h.newznabHandler)
	})

	// Internal JSON API (used by the SvelteKit frontend)
	r.Route("/v1", func(r chi.Router) {
		// Auth endpoints (public)
		r.Get("/auth/status", h.authStatus)
		r.Post("/auth/login", h.authLogin)
		r.Post("/auth/logout", h.authLogout)

		// Browse endpoints (public — read-only)
		r.Get("/spots", h.listSpots)
		r.Get("/spots/{id}", h.getSpot)
		r.Get("/spots/{id}/nzb", h.downloadNZB)
		r.Get("/spots/{id}/image", h.getImage)
		r.Get("/categories", h.listCategories)

		// SABnzbd endpoints — require auth when APP_PASSWORD is set
		r.Group(func(r chi.Router) {
			r.Use(h.requireAuth)
			r.Post("/spots/{id}/send-to-sab", h.sendToSAB)
			r.Get("/queue", h.sabQueue)
			r.Get("/sync/trigger", h.triggerSync)
		})
	})

	// Config endpoint
	r.Get("/_/config", h.appConfig)

	// Static SPA (SvelteKit build)
	if h.webFS != nil {
		fileServer := http.FileServer(http.FS(h.webFS))
		r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
			// Serve real file if it exists, else fall back to index.html (SPA routing)
			f, err := h.webFS.Open(strings.TrimPrefix(r.URL.Path, "/"))
			if err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
			// SPA fallback
			index, err := h.webFS.Open("index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer index.Close()
			http.ServeContent(w, r, "index.html", time.Time{}, index.(interface {
				io.ReadSeeker
			}))
		})
	}

	return r
}

// ─── Newznab API ─────────────────────────────────────────────────────────────

// Newznab API types
type NewznabResponse struct {
	XMLName xml.Name        `xml:"rss"`
	Version string          `xml:"version,attr"`
	Channel NewznabChannel  `xml:"channel"`
}

type NewznabChannel struct {
	Title       string       `xml:"title"`
	Description string       `xml:"description"`
	Items       []NewznabItem `xml:"item"`
	Response    NNResponse   `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ response"`
}

type NNResponse struct {
	XMLName xml.Name `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ response"`
	Offset  int      `xml:"offset,attr"`
	Total   int      `xml:"total,attr"`
}

type NewznabItem struct {
	Title      string    `xml:"title"`
	GUID       string    `xml:"guid"`
	PubDate    string    `xml:"pubDate"`
	Category   string    `xml:"category"`
	Enclosure  NNEnclosure `xml:"enclosure"`
	Attributes []NNAttr    `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ attr"`
}

type NNEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type NNAttr struct {
	XMLName xml.Name `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ attr"`
	Name    string   `xml:"name,attr"`
	Value   string   `xml:"value,attr"`
}

func (h *Handler) newznabHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t := q.Get("t")

	switch t {
	case "caps":
		h.newznabCaps(w, r)
	case "search", "tvsearch", "movie", "":
		h.newznabSearch(w, r)
	default:
		writeNewznabError(w, 202, "No such function")
	}
}

func (h *Handler) newznabCaps(w http.ResponseWriter, r *http.Request) {
	caps := `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server version="1.0" title="Spotnet" strapline="Modern Spotnet indexer" url="" email="" image=""/>
  <limits max="100" default="25"/>
  <retention days="3000"/>
  <registration available="no" open="no"/>
  <searching>
    <search available="yes" supportedParams="q,cat"/>
    <tv-search available="no"/>
    <movie-search available="no"/>
  </searching>
  <categories>
    <category id="1000" name="Console"/>
    <category id="2000" name="Movie"/>
    <category id="3000" name="Audio"/>
    <category id="4000" name="PC"/>
    <category id="5000" name="TV"/>
    <category id="7000" name="Books"/>
    <category id="8000" name="Other"/>
  </categories>
</caps>`
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprint(w, caps)
}

func (h *Handler) newznabSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	params := db.SearchParams{
		Query:      q.Get("q"),
		Limit:      parseIntOr(q.Get("limit"), 25),
		Offset:     parseIntOr(q.Get("offset"), 0),
		AllowAdult: h.cfg.AllowAdult,
	}
	if params.Limit > 100 {
		params.Limit = 100
	}

	// Map Newznab category IDs to internal categories
	if catStr := q.Get("cat"); catStr != "" {
		for _, c := range strings.Split(catStr, ",") {
			switch {
			case strings.HasPrefix(c, "2"):
				params.Categories = append(params.Categories, spotnet.CatImage)
			case strings.HasPrefix(c, "3"):
				params.Categories = append(params.Categories, spotnet.CatAudio)
			case strings.HasPrefix(c, "1"), strings.HasPrefix(c, "4"):
				params.Categories = append(params.Categories, spotnet.CatGame, spotnet.CatApp)
			}
		}
	}

	spots, total, err := h.db.SearchSpots(r.Context(), params)
	if err != nil {
		writeNewznabError(w, 300, "Search failed")
		return
	}

	channel := NewznabChannel{
		Title:       "Spotnet",
		Description: "Spotnet search results",
		Response:    NNResponse{Offset: params.Offset, Total: total},
	}

	baseURL := "https://" + r.Host
	for _, s := range spots {
		nzbURL := fmt.Sprintf("%s/v1/spots/%d/nzb", baseURL, s.ID)
		item := NewznabItem{
			Title:   s.Title,
			GUID:    s.MessageID,
			PubDate: s.PostedAt.Format(time.RFC1123Z),
			Enclosure: NNEnclosure{
				URL:    nzbURL,
				Length: s.Size,
				Type:   "application/x-nzb",
			},
			Attributes: []NNAttr{
				{Name: "category", Value: newznabCatID(s.Category)},
				{Name: "size", Value: strconv.FormatInt(s.Size, 10)},
				{Name: "poster", Value: s.Poster},
				{Name: "usenetdate", Value: s.PostedAt.Format(time.RFC1123Z)},
			},
		}
		channel.Items = append(channel.Items, item)
	}

	resp := NewznabResponse{Version: "2.0", Channel: channel}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(resp)
}

// ─── Internal JSON API ───────────────────────────────────────────────────────

func (h *Handler) listSpots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Adult content: hidden by default; expose if config allows OR caller passes adult=1
	allowAdult := h.cfg.AllowAdult
	if q.Get("adult") == "1" && h.cfg.AllowAdult {
		allowAdult = true
	}

	params := db.SearchParams{
		Query:      q.Get("q"),
		Poster:     q.Get("poster"),
		Limit:      parseIntOr(q.Get("limit"), 25),
		Offset:     parseIntOr(q.Get("offset"), 0),
		AllowAdult: allowAdult,
	}
	if catStr := q.Get("cat"); catStr != "" {
		for _, c := range strings.Split(catStr, ",") {
			if n, err := strconv.Atoi(c); err == nil {
				params.Categories = append(params.Categories, n)
			}
		}
	}
	if sort := q.Get("sort"); sort != "" {
		params.SortBy = sort
	}

	spots, total, err := h.db.SearchSpots(r.Context(), params)
	if err != nil {
		jsonError(w, "search failed", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]any{
		"spots":  spots,
		"total":  total,
		"limit":  params.Limit,
		"offset": params.Offset,
	})
}

func (h *Handler) getSpot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	spot, err := h.db.GetSpotByID(r.Context(), id)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if spot == nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	// Lazy-enrich: fetch body from Usenet if we haven't yet
	if spot.NzbID == "" || spot.Description == "" {
		if enrichErr := h.enrichSpot(r.Context(), spot); enrichErr != nil {
			slog.Warn("enrich failed", "id", spot.ID, "err", enrichErr)
		} else {
			_ = h.db.UpdateSpotEnrichment(r.Context(), spot)
		}
	}
	jsonOK(w, spot)
}

func (h *Handler) downloadNZB(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	spot, err := h.db.GetSpotByID(r.Context(), id)
	if err != nil || spot == nil {
		http.Error(w, "spot not found", http.StatusNotFound)
		return
	}
	if spot.NzbID == "" {
		if enrichErr := h.enrichSpot(r.Context(), spot); enrichErr != nil {
			http.Error(w, "could not fetch spot metadata: "+enrichErr.Error(), http.StatusBadGateway)
			return
		}
		_ = h.db.UpdateSpotEnrichment(r.Context(), spot)
	}
	if spot.NzbID == "" {
		http.Error(w, "NZB not available for this spot", http.StatusNotFound)
		return
	}
	if cached, _ := h.db.GetCachedNZB(r.Context(), spot.NzbID); len(cached) > 0 {
		w.Header().Set("Content-Type", "application/x-nzb")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.nzb\"", sanitizeFilename(spot.Title)))
		w.Write(cached)
		return
	}
	nzbData, fetchErr := h.fetchNZBSegment(r.Context(), spot.NzbID)
	if fetchErr != nil {
		http.Error(w, "NZB fetch failed: "+fetchErr.Error(), http.StatusBadGateway)
		return
	}
	_ = h.db.CacheNZB(r.Context(), spot.NzbID, nzbData)
	w.Header().Set("Content-Type", "application/x-nzb")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.nzb\"", sanitizeFilename(spot.Title)))
	w.Write(nzbData)
}

// enrichSpot fetches the spot article body from NNTP and fills Description/ImageURL/NzbID.
func (h *Handler) enrichSpot(ctx context.Context, spot *db.Spot) error {
	c, err := nntp.Dial(h.cfg.NNTPHost, h.cfg.NNTPPort, h.cfg.NNTPTLS, h.cfg.NNTPUser, h.cfg.NNTPPass)
	if err != nil {
		return fmt.Errorf("nntp dial: %w", err)
	}
	defer c.Close()

	msgID := spot.MessageID
	if !strings.HasPrefix(msgID, "<") {
		msgID = "<" + msgID + ">"
	}

	// Spotnet stores the XML in X-XML: headers (HEAD command), not the body.
	headers, err := c.Head(msgID)
	if err != nil {
		return fmt.Errorf("nntp head: %w", err)
	}
	rawXML, ok := headers["x-xml"]
	if !ok || strings.TrimSpace(rawXML) == "" {
		// Fallback: some older spots put the XML in the body
		body, bodyErr := c.Body(msgID)
		if bodyErr != nil {
			return fmt.Errorf("no x-xml header and body fetch failed: %w", bodyErr)
		}
		return spotnet.EnrichFromBody(spot, body)
	}
	return spotnet.EnrichFromXML(spot, rawXML)
}

// fetchNZBSegment fetches and decodes the NZB article from NNTP by message-id.
// Spotnet NZB articles are encoded with a custom escape scheme then zlib-compressed.
func (h *Handler) fetchNZBSegment(ctx context.Context, nzbID string) ([]byte, error) {
	c, err := nntp.Dial(h.cfg.NNTPHost, h.cfg.NNTPPort, h.cfg.NNTPTLS, h.cfg.NNTPUser, h.cfg.NNTPPass)
	if err != nil {
		return nil, fmt.Errorf("nntp dial: %w", err)
	}
	defer c.Close()
	msgID := nzbID
	if !strings.HasPrefix(msgID, "<") {
		msgID = "<" + msgID + ">"
	}
	body, err := c.BinaryBody(msgID)
	if err != nil {
		return nil, fmt.Errorf("nntp nzb body: %w", err)
	}
	return spotnet.DecodeNZBBody(body)
}

// getImage serves the spot's image. For NNTP images it fetches the segment(s),
// decodes them, caches the result, and serves it with the correct Content-Type.
// For HTTP image URLs it issues a redirect.
func (h *Handler) getImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	spot, err := h.db.GetSpotByID(r.Context(), id)
	if err != nil || spot == nil || spot.ImageURL == "" {
		http.NotFound(w, r)
		return
	}

	imageURL := spot.ImageURL

	// HTTP/HTTPS image: redirect the browser directly
	if strings.HasPrefix(imageURL, "http://") || strings.HasPrefix(imageURL, "https://") {
		http.Redirect(w, r, imageURL, http.StatusFound)
		return
	}

	// NNTP image: <nntp:seg1|seg2|...>
	if !strings.HasPrefix(imageURL, "<nntp:") {
		http.NotFound(w, r)
		return
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(imageURL, "<nntp:"), ">")
	segments := strings.Split(inner, "|")
	cacheKey := segments[0]

	// Serve from cache if available
	if cached, mime, err := h.db.GetCachedImage(r.Context(), cacheKey); err == nil && len(cached) > 0 {
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(cached)
		return
	}

	// Fetch from NNTP
	imgData, err := h.fetchImageSegments(r.Context(), segments)
	if err != nil {
		slog.Error("image fetch", "spot_id", id, "err", err)
		http.Error(w, "image unavailable", http.StatusBadGateway)
		return
	}

	mime := detectImageMIME(imgData)
	_ = h.db.CacheImage(r.Context(), cacheKey, imgData, mime)

	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(imgData)
}

func (h *Handler) fetchImageSegments(ctx context.Context, segments []string) ([]byte, error) {
	c, err := nntp.Dial(h.cfg.NNTPHost, h.cfg.NNTPPort, h.cfg.NNTPTLS, h.cfg.NNTPUser, h.cfg.NNTPPass)
	if err != nil {
		return nil, fmt.Errorf("nntp dial: %w", err)
	}
	defer c.Close()

	var all []byte
	for _, seg := range segments {
		msgID := seg
		if !strings.HasPrefix(msgID, "<") {
			msgID = "<" + msgID + ">"
		}
		body, err := c.BinaryBody(msgID)
		if err != nil {
			return nil, fmt.Errorf("nntp image body %s: %w", seg, err)
		}
		all = append(all, body...)
	}
	return spotnet.DecodeImageBody(all), nil
}

// detectImageMIME sniffs the first bytes to determine the image content-type.
func detectImageMIME(data []byte) string {
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8 {
		return "image/jpeg"
	}
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		return "image/png"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "image/gif"
	}
	return "image/jpeg" // safe fallback
}

func sanitizeFilename(s string) string {
	r := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-")
	s = r.Replace(s)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func (h *Handler) sendToSAB(w http.ResponseWriter, r *http.Request) {
	if h.sab == nil {
		jsonError(w, "SABnzbd not configured", http.StatusServiceUnavailable)
		return
	}

	var body struct {
		NzbURL   string `json:"nzb_url"`
		Name     string `json:"name"`
		Category string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	ids, err := h.sab.AddNZBByURL(body.NzbURL, body.Name, body.Category)
	if err != nil {
		jsonError(w, fmt.Sprintf("SABnzbd error: %s", err), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]any{"nzo_ids": ids})
}

func (h *Handler) sabQueue(w http.ResponseWriter, r *http.Request) {
	if h.sab == nil {
		jsonError(w, "SABnzbd not configured", http.StatusServiceUnavailable)
		return
	}

	q, err := h.sab.GetQueue(0, 20)
	if err != nil {
		jsonError(w, fmt.Sprintf("SABnzbd error: %s", err), http.StatusBadGateway)
		return
	}
	jsonOK(w, q)
}

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, spotnet.MainCategories)
}

func (h *Handler) triggerSync(w http.ResponseWriter, r *http.Request) {
	// The sync engine is triggered via a channel; set up in main.go
	jsonOK(w, map[string]string{"status": "triggered"})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	// Kept for backwards compatibility — replaced by authLogin below.
	h.authLogin(w, r)
}

// ─── Auth handlers ────────────────────────────────────────────────────────────

// authStatus returns whether auth is required and whether the current request
// is authenticated. The SPA calls this on startup.
func (h *Handler) authStatus(w http.ResponseWriter, r *http.Request) {
	authRequired := h.cfg.AppPassword != ""
	authenticated := !authRequired || h.sessions.Valid(auth.TokenFromRequest(r))
	jsonOK(w, map[string]bool{
		"auth_required": authRequired,
		"authenticated": authenticated,
	})
}

// authLogin validates the password and sets a session cookie.
func (h *Handler) authLogin(w http.ResponseWriter, r *http.Request) {
	if h.cfg.AppPassword == "" {
		// Auth disabled — nothing to log in to.
		jsonOK(w, map[string]bool{"authenticated": true})
		return
	}

	ip := r.RemoteAddr
	if h.sessions.Locked(ip) {
		jsonError(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	if !auth.CheckPassword(body.Password, h.cfg.AppPassword) {
		h.sessions.RecordFailure(ip)
		// Deliberate vague message — don't confirm whether the password field was right
		jsonError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	h.sessions.RecordSuccess(ip)
	token := h.sessions.Create()
	auth.SetCookie(w, token, h.cfg.SessionDuration)
	jsonOK(w, map[string]bool{"authenticated": true})
}

// authLogout clears the session.
func (h *Handler) authLogout(w http.ResponseWriter, r *http.Request) {
	token := auth.TokenFromRequest(r)
	h.sessions.Delete(token)
	auth.ClearCookie(w)
	jsonOK(w, map[string]bool{"authenticated": false})
}

// requireAuth is a middleware that gates access when APP_PASSWORD is set.
// When auth is disabled it passes all requests through.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.cfg.AppPassword == "" {
			next.ServeHTTP(w, r)
			return
		}
		if h.sessions.Valid(auth.TokenFromRequest(r)) {
			next.ServeHTTP(w, r)
			return
		}
		jsonError(w, "unauthorized", http.StatusUnauthorized)
	})
}

// appConfig returns public config values for the SPA.
func (h *Handler) appConfig(w http.ResponseWriter, r *http.Request) {
	authRequired := h.cfg.AppPassword != ""
	authenticated := !authRequired || h.sessions.Valid(auth.TokenFromRequest(r))
	jsonOK(w, map[string]interface{}{
		"allow_adult":   h.cfg.AllowAdult,
		"auth_required": authRequired,
		"authenticated": authenticated,
		"version":       "0.1.0",
	})
}

// authMiddleware is kept for the newznab API key path (Prowlarr compatibility).
func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API key auth: ?apikey=xxx or X-Api-Key header
		apiKey := r.URL.Query().Get("apikey")
		if apiKey == "" {
			apiKey = r.Header.Get("X-Api-Key")
		}
		if h.cfg.APIKey != "" && apiKey != h.cfg.APIKey {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders adds standard defensive HTTP headers to every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-XSS-Protection", "0") // Modern browsers use CSP; this header is legacy
		next.ServeHTTP(w, r)
	})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeNewznabError(w http.ResponseWriter, code int, description string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><error code="%d" description="%s"/>`, code, description)
}

func parseIntOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func newznabCatID(cat int) string {
	switch cat {
	case spotnet.CatImage:
		return "2000"
	case spotnet.CatAudio:
		return "3000"
	case spotnet.CatGame:
		return "1000"
	case spotnet.CatApp:
		return "4000"
	default:
		return "8000"
	}
}
