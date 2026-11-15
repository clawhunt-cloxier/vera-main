package httpapi

import (
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

// Until 0.4 is implemented, do not silently serve a 0.3 plan or task to a
// client that requires the new representation. Wildcards and application/json
// continue to select 0.3; a 0.4 client must check the response Content-Type.
func researchVersionGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !researchVersionPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept")
		if !acceptsLegacyJSON(r.Header.Values("Accept")) {
			httpx.Error(w, r, errs.New(http.StatusNotAcceptable, "REPRESENTATION_UNAVAILABLE", "请求的数据版本尚不可用"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func researchVersionPath(path string) bool {
	for _, base := range []string{"/api/v1/research-plans", "/api/v1/research-tasks"} {
		if path == base || strings.HasPrefix(path, base+"/") {
			return true
		}
	}
	return false
}

func acceptsLegacyJSON(headers []string) bool {
	if len(headers) == 0 || strings.TrimSpace(strings.Join(headers, "")) == "" {
		return true
	}
	// The most specific matching range determines the quality of JSON. A
	// specific q=0 exclusion overrides a more permissive wildcard.
	bestSpecificity, bestQuality := -1, 0.0
	for _, part := range strings.Split(strings.Join(headers, ","), ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		specificity := -1
		switch strings.ToLower(media) {
		case "application/json":
			specificity = 2
		case "application/*":
			specificity = 1
		case "*/*":
			specificity = 0
		}
		if specificity < 0 {
			continue
		}
		quality := 1.0
		if q, ok := params["q"]; ok {
			quality, err = strconv.ParseFloat(q, 64)
			if err != nil || math.IsNaN(quality) || quality < 0 || quality > 1 {
				continue
			}
		}
		if specificity > bestSpecificity || (specificity == bestSpecificity && quality > bestQuality) {
			bestSpecificity, bestQuality = specificity, quality
		}
	}
	return bestSpecificity >= 0 && bestQuality > 0
}
