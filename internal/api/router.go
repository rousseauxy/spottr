package api

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
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

// Version is reported by /_/config and the newznab caps document. Keep it in
// step with the release tag.
const Version = "0.2.0"

// Handler holds all dependencies for HTTP handlers.
type Handler struct {
	cfg      *config.Config
	db       *db.DB
	sab      *sabnzbd.Client
	webFS    fs.FS
	sessions *auth.Store
	pool     *nntp.Pool
	log      *slog.Logger

	// apiKey is the effective newznab key: the API_KEY override when set,
	// otherwise the value persisted in the database.
	apiKey string

	limiter *rateLimiter

	// csp is derived from the built frontend at construction time.
	csp string
}

// NewHandler constructs a Handler with all dependencies injected.
func NewHandler(cfg *config.Config, database *db.DB, sab *sabnzbd.Client, webFS fs.FS,
	pool *nntp.Pool, apiKey string, log *slog.Logger) *Handler {
	policy := buildCSP(webFS)
	log.Info("content security policy", "script_src", describeCSP(policy))

	return &Handler{
		cfg:      cfg,
		db:       database,
		sab:      sab,
		webFS:    webFS,
		sessions: auth.New(cfg.SessionDuration),
		pool:     pool,
		log:      log,
		apiKey:   apiKey,
		limiter:  newRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst),
		csp:      policy,
	}
}

// Router returns the fully configured chi router.
func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()
	// Our own realIP replaces middleware.RealIP, which trusts forwarding
	// headers from any peer. See middleware.go.
	r.Use(realIP(h.cfg.TrustedProxies))
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(h.log))
	r.Use(middleware.Compress(5))
	r.Use(securityHeaders(h.csp))

	// Liveness/readiness. Unauthenticated and dependency-light so the
	// container healthcheck does not depend on the newznab API being open.
	r.Get("/healthz", h.healthz)
	r.Get("/readyz", h.readyz)

	// Newznab-compatible API (used by Prowlarr/Sonarr/Radarr).
	// requireAPIKey was previously written but never mounted, leaving the whole
	// newznab surface open to anonymous callers.
	r.Route("/api", func(r chi.Router) {
		r.Use(h.limiter.middleware)
		r.Use(h.requireAPIKey)
		r.Get("/", h.newznabHandler)
		r.Get("/newznab", h.newznabHandler)
	})

	// Internal JSON API (used by the SvelteKit frontend)
	r.Route("/v1", func(r chi.Router) {
		r.Use(h.limiter.middleware)

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

		// Privileged endpoints — these reach out to SABnzbd or the database on
		// the caller's behalf.
		r.Group(func(r chi.Router) {
			r.Use(h.requireAuth)
			r.Post("/spots/{id}/send-to-sab", h.sendToSAB)
			r.Get("/queue", h.sabQueue)
			r.Get("/sab/categories", h.sabCategories)
			r.Get("/metrics", h.metricsHandler)
		})
	})

	// Config endpoint
	r.Get("/_/config", h.appConfig)

	// Static SPA (SvelteKit build)
	if h.webFS != nil {
		r.Get("/*", spaHandler(h.webFS))
	}

	return r
}

// spaHandler serves the SvelteKit build: a file that exists is served as it is, and any other path
// without a file extension gets index.html, so the client-side router can handle it. A missing path
// WITH an extension (/favicon.svg, /x.js) is a 404: answering it with index.html and a 200 let
// Cloudflare cache the app page under an image's URL for four hours, since it caches by extension.
func spaHandler(webFS fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(webFS))
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if f, err := webFS.Open(name); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		index, err := webFS.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer index.Close()
		http.ServeContent(w, r, "index.html", time.Time{}, index.(interface {
			io.ReadSeeker
		}))
	}
}

// ─── Newznab API ─────────────────────────────────────────────────────────────

// Newznab API types
type NewznabResponse struct {
	XMLName xml.Name       `xml:"rss"`
	Version string         `xml:"version,attr"`
	Channel NewznabChannel `xml:"channel"`
}

type NewznabChannel struct {
	Title       string        `xml:"title"`
	Description string        `xml:"description"`
	Items       []NewznabItem `xml:"item"`
	Response    NNResponse    `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ response"`
}

type NNResponse struct {
	XMLName xml.Name `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ response"`
	Offset  int      `xml:"offset,attr"`
	Total   int      `xml:"total,attr"`
}

type NewznabItem struct {
	Title      string      `xml:"title"`
	GUID       string      `xml:"guid"`
	PubDate    string      `xml:"pubDate"`
	Category   string      `xml:"category"`
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
  <server version="` + Version + `" title="Spottr" strapline="Modern Spotnet indexer" url="" email="" image=""/>
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

	limit, offset := h.clampPaging(parseIntOr(q.Get("limit"), 25), parseIntOr(q.Get("offset"), 0))
	if limit > 100 {
		limit = 100 // newznab advertises max="100" in caps
	}
	params := db.SearchParams{
		Query:      q.Get("q"),
		Limit:      limit,
		Offset:     offset,
		AllowAdult: h.cfg.AllowAdult,
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

// clampPaging bounds caller-supplied pagination.
//
// limit was previously unbounded on this endpoint (only newznab capped it), so
// ?limit=99999 would materialise 99,999 rows per request; offset was likewise
// free, making deep pagination an easy way to force large scans.
func (h *Handler) clampPaging(limit, offset int) (int, int) {
	if limit < 1 {
		limit = 25
	}
	if limit > h.cfg.MaxPageSize {
		limit = h.cfg.MaxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	if offset > h.cfg.MaxOffset {
		offset = h.cfg.MaxOffset
	}
	return limit, offset
}

// includeAdult decides whether a listing shows 18+ spots: only on the 18+ filter (kind=erotic, and
// nothing else asked for alongside it) while the server allows 18+ spots (ALLOW_ADULT). Every
// other listing and search leaves them out, also with ALLOW_ADULT on. Until 2026-10-17 an allowing
// server showed them everywhere; 0.6.1 needed a checkbox; the owner wants them only on their tab.
func includeAdult(serverAllows bool, kinds []int) bool {
	return serverAllows && len(kinds) == 1 && kinds[0] == db.KindErotic
}

// hiddenAdult reports whether a spot must not be served at all: 18+ while the server does not
// allow 18+ spots. A spot's page, image and NZB are reachable by id, not only through a listing.
func (h *Handler) hiddenAdult(spot *db.Spot) bool {
	return spot != nil && spot.IsAdult && !h.cfg.AllowAdult
}

func (h *Handler) listSpots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit, offset := h.clampPaging(parseIntOr(q.Get("limit"), 25), parseIntOr(q.Get("offset"), 0))

	params := db.SearchParams{
		Query:  q.Get("q"),
		Poster: q.Get("poster"),
		Limit:  limit,
		Offset: offset,
	}
	// Keyset pagination: `before` is a unix timestamp and lets a client page
	// deeply without paying for a growing OFFSET.
	if before := parseIntOr(q.Get("before"), 0); before > 0 {
		params.Before = time.Unix(int64(before), 0)
	}
	if catStr := q.Get("cat"); catStr != "" {
		for _, c := range strings.Split(catStr, ",") {
			if n, err := strconv.Atoi(c); err == nil {
				params.Categories = append(params.Categories, n)
			}
		}
	}
	// tag=<exact tag>; sub=a5,c2 (subcategory codes, all required): the links on a spot's page.
	params.Tag = strings.TrimSpace(q.Get("tag"))
	if subStr := q.Get("sub"); subStr != "" {
		for _, c := range strings.Split(subStr, ",") {
			if c = strings.ToLower(strings.TrimSpace(c)); db.ValidSubCode(c) {
				params.SubCodes = append(params.SubCodes, c)
			}
		}
	}
	// kind=movie,series,book,comic,erotic narrows the Image category by its Spotnet type code.
	if kindStr := q.Get("kind"); kindStr != "" {
		for _, k := range strings.Split(kindStr, ",") {
			if n, ok := db.KindNames[strings.TrimSpace(k)]; ok {
				params.Kinds = append(params.Kinds, n)
			}
		}
	}
	params.AllowAdult = includeAdult(h.cfg.AllowAdult, params.Kinds)
	params.AdultOnly = params.AllowAdult
	if sort := q.Get("sort"); sort != "" {
		params.SortBy = sort
	}

	spots, total, err := h.db.SearchSpots(r.Context(), params)
	if err != nil {
		h.log.Error("search failed", "err", err)
		jsonError(w, "search failed", http.StatusInternalServerError)
		return
	}

	resp := map[string]any{
		"spots":  spots,
		"total":  total,
		"limit":  params.Limit,
		"offset": params.Offset,
	}
	// Cursor for the next page, so clients can avoid OFFSET entirely.
	if n := len(spots); n > 0 {
		resp["next_before"] = spots[n-1].PostedAt.Unix()
	}
	jsonOK(w, resp)
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
	if spot == nil || h.hiddenAdult(spot) {
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
	// PasswordHint: a password the poster wrote in the description, as a suggestion for the
	// send-to-SABnzbd dialog; "" when the description gives none.
	jsonOK(w, struct {
		*db.Spot
		PasswordHint string
	}{spot, spotnet.PasswordHint(spot.Description)})
}

// nzbStatusError carries an HTTP status alongside the failure reason so callers
// can surface the same distinction downloadNZB used to make inline.
type nzbStatusError struct {
	status int
	msg    string
}

func (e *nzbStatusError) Error() string { return e.msg }

// resolveNZB loads a spot by id and returns it together with the decoded NZB
// bytes, enriching from NNTP and populating the cache as needed. Both the
// browser download and the SABnzbd push go through this, so they can never
// disagree about what a spot's NZB is.
func (h *Handler) resolveNZB(ctx context.Context, id int64) (*db.Spot, []byte, error) {
	spot, err := h.db.GetSpotByID(ctx, id)
	if err != nil || spot == nil || h.hiddenAdult(spot) {
		return nil, nil, &nzbStatusError{http.StatusNotFound, "spot not found"}
	}
	if spot.NzbID == "" {
		if enrichErr := h.enrichSpot(ctx, spot); enrichErr != nil {
			return nil, nil, &nzbStatusError{http.StatusBadGateway, "could not fetch spot metadata: " + enrichErr.Error()}
		}
		_ = h.db.UpdateSpotEnrichment(ctx, spot)
	}
	if spot.NzbID == "" {
		return nil, nil, &nzbStatusError{http.StatusNotFound, "NZB not available for this spot"}
	}
	if cached, _ := h.db.GetCachedNZB(ctx, spot.NzbID); len(cached) > 0 {
		metricNZBCacheHits.Add(1)
		return spot, cached, nil
	}
	metricNZBFetches.Add(1)
	nzbData, fetchErr := h.fetchNZBSegment(ctx, spot.NzbID)
	if fetchErr != nil {
		metricNZBFailures.Add(1)
		h.log.Warn("nzb fetch failed", "spot_id", id, "err", fetchErr)
		// Do not echo the upstream error to the caller: it can carry NNTP
		// server responses and internal host detail.
		return nil, nil, &nzbStatusError{http.StatusBadGateway, "NZB fetch failed"}
	}
	_ = h.db.CacheNZB(ctx, spot.NzbID, nzbData)
	return spot, nzbData, nil
}

// writeNZBError maps a resolveNZB failure onto the response.
func writeNZBError(w http.ResponseWriter, err error, asJSON bool) {
	status := http.StatusBadGateway
	msg := err.Error()
	var se *nzbStatusError
	if errors.As(err, &se) {
		status = se.status
	}
	if asJSON {
		jsonError(w, msg, status)
		return
	}
	http.Error(w, msg, status)
}

func (h *Handler) downloadNZB(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	spot, nzbData, err := h.resolveNZB(r.Context(), id)
	if err != nil {
		writeNZBError(w, err, false)
		return
	}
	w.Header().Set("Content-Type", "application/x-nzb")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.nzb\"", sanitizeFilename(spot.Title)))
	w.Write(nzbData)
}

// leaseNNTP borrows a pooled connection with the configured article ceiling
// applied. Callers must Release it.
func (h *Handler) leaseNNTP(ctx context.Context) (*nntp.Conn, error) {
	conn, err := h.pool.Get(ctx)
	if err != nil {
		metricNNTPDialFail.Add(1)
		return nil, fmt.Errorf("nntp: %w", err)
	}
	conn.SetMaxArticleBytes(h.cfg.MaxArticleBytes)
	return conn, nil
}

// enrichSpot fetches the spot article headers from NNTP and fills
// Description/ImageURL/NzbID, plus the spotter identity and signature status.
func (h *Handler) enrichSpot(ctx context.Context, spot *db.Spot) error {
	conn, err := h.leaseNNTP(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	// Spotnet stores the XML in X-XML: headers (HEAD command), not the body.
	headers, err := conn.Head(spot.MessageID)
	if err != nil {
		conn.MarkBroken()
		return fmt.Errorf("nntp head: %w", err)
	}
	if err := spotnet.EnrichFromHeaders(spot, headers); err == nil {
		return nil
	}

	// Fallback: some older spots put the XML in the body
	body, bodyErr := conn.Body(spot.MessageID)
	if bodyErr != nil {
		conn.MarkBroken()
		return fmt.Errorf("no x-xml header and body fetch failed: %w", bodyErr)
	}
	return spotnet.EnrichFromBody(spot, body)
}

// fetchNZBSegment fetches and decodes the NZB article from NNTP by message-id.
// Spotnet NZB articles are encoded with a custom escape scheme then deflated.
func (h *Handler) fetchNZBSegment(ctx context.Context, nzbID string) ([]byte, error) {
	conn, err := h.leaseNNTP(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	body, err := conn.BinaryBody(nzbID)
	if err != nil {
		conn.MarkBroken()
		return nil, fmt.Errorf("nntp nzb body: %w", err)
	}
	return spotnet.DecodeNZBBodyLimit(body, h.cfg.MaxNZBBytes)
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
	if err != nil || spot == nil || spot.ImageURL == "" || h.hiddenAdult(spot) {
		http.NotFound(w, r)
		return
	}

	imageURL := spot.ImageURL

	// HTTP/HTTPS image URLs come from Spotnet article XML, which any poster
	// controls. Redirecting to them turned this endpoint into an open redirect
	// on our own origin — a phishing primitive that also leaks the visitor to
	// an arbitrary third party. We do not proxy them either: that would be a
	// blind SSRF fetcher. Such spots simply have no served image.
	if strings.HasPrefix(imageURL, "http://") || strings.HasPrefix(imageURL, "https://") {
		http.NotFound(w, r)
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

// maxImageSegments bounds how many articles one image may span. The segment
// list is attacker-supplied, so an unbounded loop is an amplification lever:
// one request could otherwise trigger hundreds of upstream fetches.
const maxImageSegments = 16

func (h *Handler) fetchImageSegments(ctx context.Context, segments []string) ([]byte, error) {
	if len(segments) > maxImageSegments {
		return nil, fmt.Errorf("image has too many segments (%d)", len(segments))
	}

	conn, err := h.leaseNNTP(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	var all []byte
	for _, seg := range segments {
		// BinaryBody validates and brackets the id itself.
		body, err := conn.BinaryBody(seg)
		if err != nil {
			conn.MarkBroken()
			return nil, fmt.Errorf("nntp image body: %w", err)
		}
		if int64(len(all))+int64(len(body)) > h.cfg.MaxArticleBytes {
			return nil, nntp.ErrArticleTooLarge
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

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Name, category and password are optional; an absent or empty body is fine.
	var body struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Password string `json:"password"`
	}
	if decErr := json.NewDecoder(r.Body).Decode(&body); decErr != nil && decErr != io.EOF {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	// Fetch the NZB ourselves and upload the bytes to SABnzbd. Handing SAB a URL
	// instead would make it fetch spottr over the network, which fails whenever
	// spottr sits behind an auth proxy — SAB receives the login page, not an NZB.
	spot, nzbData, err := h.resolveNZB(r.Context(), id)
	if err != nil {
		writeNZBError(w, err, true)
		return
	}

	name := body.Name
	if name == "" {
		name = spot.Title
	}

	ids, err := h.sab.AddNZBContent(nzbData, sanitizeFilename(name), body.Category, strings.TrimSpace(body.Password))
	if err != nil {
		jsonError(w, fmt.Sprintf("SABnzbd error: %s", err), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]any{"nzo_ids": ids})
}

func (h *Handler) sabCategories(w http.ResponseWriter, r *http.Request) {
	if h.sab == nil {
		jsonError(w, "SABnzbd not configured", http.StatusServiceUnavailable)
		return
	}
	cats, err := h.sab.Categories()
	if err != nil {
		jsonError(w, fmt.Sprintf("SABnzbd error: %s", err), http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]any{"categories": cats})
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

// ─── Auth handlers ────────────────────────────────────────────────────────────

// authStatus returns whether auth is required and whether the current request
// is authenticated. The SPA calls this on startup.
func (h *Handler) authStatus(w http.ResponseWriter, r *http.Request) {
	authRequired := h.cfg.AuthEnforced()
	authenticated := !authRequired || h.sessions.Valid(auth.TokenFromRequest(r))
	jsonOK(w, map[string]bool{
		"auth_required": authRequired,
		"authenticated": authenticated,
	})
}

// authLogin validates the password and sets a session cookie.
func (h *Handler) authLogin(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.AuthEnforced() {
		// Auth delegated to a proxy, or deliberately disabled.
		jsonOK(w, map[string]bool{"authenticated": true})
		return
	}

	ip := hostOnly(r.RemoteAddr)
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
		metricAuthFailures.Add(1)
		// Deliberate vague message — don't confirm whether the password field was right
		jsonError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	h.sessions.RecordSuccess(ip)
	token := h.sessions.Create()
	auth.SetCookie(w, token, h.cfg.SessionDuration, h.cfg.SecureCookie)
	jsonOK(w, map[string]bool{"authenticated": true})
}

// authLogout clears the session.
func (h *Handler) authLogout(w http.ResponseWriter, r *http.Request) {
	token := auth.TokenFromRequest(r)
	h.sessions.Delete(token)
	auth.ClearCookie(w)
	jsonOK(w, map[string]bool{"authenticated": false})
}

// requireAuth gates the privileged endpoints.
//
// This no longer fails open on an empty password. Previously an unset
// APP_PASSWORD silently disabled the check, so a deployment that simply forgot
// to configure one exposed SABnzbd control and the download queue to anonymous
// callers. Running without in-app auth is now an explicit choice made in
// config (AUTH_MODE=proxy or open), validated at startup.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.cfg.AuthEnforced() {
			// AUTH_MODE=proxy or open: the operator has asserted that
			// something in front of us is responsible for authentication.
			next.ServeHTTP(w, r)
			return
		}
		if h.sessions.Valid(auth.TokenFromRequest(r)) {
			next.ServeHTTP(w, r)
			return
		}
		metricAuthFailures.Add(1)
		jsonError(w, "unauthorized", http.StatusUnauthorized)
	})
}

// appConfig returns public config values for the SPA.
func (h *Handler) appConfig(w http.ResponseWriter, r *http.Request) {
	authRequired := h.cfg.AuthEnforced()
	authenticated := !authRequired || h.sessions.Valid(auth.TokenFromRequest(r))
	jsonOK(w, map[string]interface{}{
		"allow_adult":   h.cfg.AllowAdult,
		"auth_required": authRequired,
		"authenticated": authenticated,
		"version":       Version,
	})
}

// requireAPIKey gates the newznab API. Accepts ?apikey= (what Prowlarr and
// friends send) or an X-Api-Key header.
//
// The key is compared in constant time, and a missing key is rejected rather
// than waved through — the previous implementation only compared when a key
// happened to be configured, and was never mounted on the router at all.
func (h *Handler) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := r.URL.Query().Get("apikey")
		if provided == "" {
			provided = r.Header.Get("X-Api-Key")
		}
		if h.apiKey == "" || !auth.CheckPassword(provided, h.apiKey) {
			metricAuthFailures.Add(1)
			// Newznab clients expect an XML error document, not JSON.
			w.WriteHeader(http.StatusUnauthorized)
			writeNewznabError(w, 100, "Incorrect user credentials")
			return
		}
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
