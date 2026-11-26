package httpapi

import (
	"context"
	"encoding/json"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

const (
	legacyMedia      = "application/json"
	observationMedia = "application/vnd.vera.observation.v1+json"
	realtimeMedia    = "application/vnd.vera.v0.4+json"
)

type representationKey struct{}

// A vendor media type must be requested explicitly. An absent Accept or a
// wildcard keeps the historical JSON behavior instead of changing plan intent.
func mediaQuality(headers []string, target string, wildcard bool) float64 {
	if strings.TrimSpace(strings.Join(headers, "")) == "" {
		if wildcard {
			return 1
		}
		return 0
	}
	bestSpecificity, bestQuality := -1, 0.0
	for _, part := range strings.Split(strings.Join(headers, ","), ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		specificity := -1
		switch {
		case strings.EqualFold(media, target):
			specificity = 2
		case wildcard && strings.EqualFold(media, "application/*"):
			specificity = 1
		case wildcard && media == "*/*":
			specificity = 0
		}
		if specificity < 0 {
			continue
		}
		quality := 1.0
		if q, ok := params["q"]; ok {
			quality, err = strconv.ParseFloat(q, 64)
			if err != nil || math.IsNaN(quality) || math.IsInf(quality, 0) || quality < 0 || quality > 1 {
				continue
			}
		}
		if specificity > bestSpecificity || (specificity == bestSpecificity && quality > bestQuality) {
			bestSpecificity, bestQuality = specificity, quality
		}
	}
	return bestQuality
}

func negotiateRepresentation(r *http.Request, realtime, observation bool) string {
	selected, quality := "", 0.0
	for _, candidate := range []string{realtimeMedia, observationMedia, legacyMedia} {
		if candidate == realtimeMedia && !realtime {
			continue
		}
		if candidate == observationMedia && !observation {
			continue
		}
		q := mediaQuality(r.Header.Values("Accept"), candidate, candidate == legacyMedia)
		if q > quality {
			selected, quality = candidate, q
		}
	}
	return selected
}

func researchVersionPath(path string) bool {
	return strings.HasPrefix(path, "/api/") && !strings.HasSuffix(path, "/pdf") && !strings.HasPrefix(path, "/api/v1/conversations")
}

func representation(r *http.Request) string {
	if selected, ok := r.Context().Value(representationKey{}).(string); ok {
		return selected
	}
	return negotiateRepresentation(r, true, true)
}

func observationVersion(r *http.Request) bool { return representation(r) == observationMedia }
func realtimeVersion(r *http.Request) bool    { return representation(r) == realtimeMedia }

func planConfirmationVersion(mode string, quoteRequirement *string, media string) bool {
	switch mode {
	case "fixed_legacy":
		return media == legacyMedia
	case "collect_latest":
		if quoteRequirement != nil {
			return media == realtimeMedia
		}
		return media == observationMedia
	default:
		return false
	}
}

func requestRepresentationMatches(r *http.Request, selected string) bool {
	if r.Method != http.MethodPost || (r.URL.Path != "/api/v1/research-plans" && r.URL.Path != "/api/v1/research-tasks") {
		return true
	}
	content := r.Header.Get("Content-Type")
	// Existing JSON clients may omit Content-Type; 0.4 creation always requires
	// the explicit request media type, independently from Accept.
	if content == "" {
		return selected != realtimeMedia
	}
	media, _, err := mime.ParseMediaType(content)
	if err != nil {
		return false
	}
	if selected == realtimeMedia {
		return strings.EqualFold(media, realtimeMedia)
	}
	return strings.EqualFold(media, legacyMedia) || (selected == observationMedia && strings.EqualFold(media, observationMedia))
}

func versionGate(next http.Handler, realtime, observation bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !researchVersionPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept")
		selected := negotiateRepresentation(r, realtime, observation)
		if selected == "" {
			httpx.Error(w, r, errs.New(http.StatusNotAcceptable, "REPRESENTATION_UNAVAILABLE", "请求的数据版本不可用"))
			return
		}
		if !requestRepresentationMatches(r, selected) {
			httpx.Error(w, r, errs.New(http.StatusUnsupportedMediaType, "REPRESENTATION_MISMATCH", "请求 Content-Type 必须与研究协议一致"))
			return
		}
		w.Header().Set("Content-Type", selected+"; charset=utf-8")
		r = r.WithContext(context.WithValue(r.Context(), representationKey{}, selected))
		if selected != realtimeMedia {
			w = &representationWriter{ResponseWriter: w, path: r.URL.Path, media: selected}
		}
		next.ServeHTTP(w, r)
	})
}

// JSON and replay handlers write one complete JSON value. Transform that value
// in Write, while the caller's source authorization transaction is still open;
// delaying a flush until after the handler returned would break revocation locks.
type representationWriter struct {
	http.ResponseWriter
	path, media string
}

func (w *representationWriter) Write(body []byte) (int, error) {
	filtered := legacyJSON(body, w.path, w.media)
	_, err := w.ResponseWriter.Write(filtered)
	if err != nil {
		return 0, err
	}
	return len(body), nil
}

func (w *representationWriter) WriteHeader(status int) {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(status)
}

func legacyJSON(body []byte, path, media string) []byte {
	if media == realtimeMedia {
		return body
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return body
	}
	fields := []string{}
	switch {
	case strings.HasPrefix(path, "/api/v1/research-plans"):
		if media == legacyMedia {
			fields = []string{"snapshot_mode", "quote_requirement"}
		}
		if plan, ok := object["plan"]; ok {
			object["plan"] = stripJSONFields(plan, fields)
			fields = nil
		}
	case strings.HasPrefix(path, "/api/v1/research-tasks"):
		fields = []string{"quote_requirement"}
		if media == legacyMedia {
			fields = append(fields, "snapshot_mode", "snapshot_as_of")
		}
		if raw, ok := object["items"]; ok {
			var items []json.RawMessage
			if json.Unmarshal(raw, &items) == nil {
				for i := range items {
					items[i] = stripJSONFields(items[i], fields)
				}
				object["items"], _ = json.Marshal(items)
			}
			fields = nil
		}
	case strings.HasPrefix(path, "/api/v1/reports/"):
		fields = []string{"source_freshness"}
		if media == legacyMedia {
			fields = append(fields, "snapshot_mode", "snapshot_as_of")
		}
	case strings.HasPrefix(path, "/api/v1/evidence/"):
		if media == legacyMedia {
			fields = []string{"event_at", "received_at", "latency_mode"}
		}
	}
	for _, field := range fields {
		delete(object, field)
	}
	result, err := json.Marshal(object)
	if err != nil {
		return body
	}
	return result
}

func stripJSONFields(body []byte, fields []string) []byte {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return body
	}
	for _, field := range fields {
		delete(object, field)
	}
	result, err := json.Marshal(object)
	if err != nil {
		return body
	}
	return result
}

func (s *Server) versionGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		realtime := s.Engine.RealtimeEnabled()
		// Discovery and new plans negotiate the available quote protocol.
		// Persisted research keeps its representation and authorization.
		capabilityRequest := r.Method == http.MethodGet && r.URL.Path == "/api/v1/me"
		newPlan := r.Method == http.MethodPost && r.URL.Path == "/api/v1/research-plans"
		if realtime && (capabilityRequest || newPlan) && mediaQuality(r.Header.Values("Accept"), realtimeMedia, false) > 0 {
			quote, err := s.quoteCapabilities(r.Context())
			if err != nil {
				httpx.Error(w, r, errs.New(http.StatusServiceUnavailable, "REALTIME_SOURCE_UNAVAILABLE", "实时行情来源不可用"))
				return
			}
			realtime = quote.Latest
		}
		versionGate(next, realtime, s.Engine.Brief != nil).ServeHTTP(w, r)
	})
}
