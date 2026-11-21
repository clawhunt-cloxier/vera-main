package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

// Starting preparation requires a selected entity from the authorized
// catalog. Typing a search query alone must never enqueue a vendor sync.
func (s *Server) prepareSubject(w http.ResponseWriter, r *http.Request) error {
	if !s.combined() || s.Engine.ChoiceCatalog == nil {
		return errs.NotFound()
	}
	if representation(r) != legacyMedia {
		return errs.New(http.StatusNotAcceptable, "REPRESENTATION_UNAVAILABLE", "请使用收盘研究协议准备数据")
	}
	var input struct{}
	if err := httpx.Decode(r, &input); err != nil {
		return err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	entities, err := s.Engine.ChoiceCatalog.Entities(r.Context(), []uuid.UUID{id})
	if err != nil {
		return err
	}
	entity, ok := entities[id]
	if !ok || entity.Synthetic || entity.Kind != "stock" || !emquantBriefCodeLike(entity.Code) {
		return errs.NotFound()
	}
	_, ws := principal(r)
	result, err := s.Engine.RequestSubjectPreparation(r.Context(), ws, entity.Code)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if result.Status == "pending" {
		status = http.StatusAccepted
		w.Header().Set("Retry-After", "5")
	}
	httpx.JSON(w, r, status, result)
	return nil
}
