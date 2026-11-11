package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/watchlist"
)

func (s *Server) watchItems(r *http.Request, ws uuid.UUID, items []watchlist.Item) ([]WatchItem, error) {
	var ids []uuid.UUID
	for _, it := range items {
		ids = append(ids, it.EntityID)
	}
	ents, err := s.Store.Entities(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	out := []WatchItem{}
	for _, it := range items {
		wi := WatchItem{ID: it.ID, Version: it.Version, Entity: toEntity(ents[it.EntityID], s.Engine.Skills), Tags: it.Tags, Note: it.Note,
			Alerts: it.Alerts, AddedAt: it.AddedAt, Snapshot: it.Snapshot}
		last, err := s.reportSummaries(r, ws, `subject_id=$1 ORDER BY delivered_at DESC LIMIT 1`, it.EntityID)
		if err != nil {
			return nil, err
		}
		if len(last) > 0 {
			wi.LastReport = &last[0]
		}
		out = append(out, wi)
	}
	return out, nil
}

func (s *Server) listWatch(w http.ResponseWriter, r *http.Request) error {
	_, ws := principal(r)
	items, err := s.Watch.List(r.Context(), ws)
	if err != nil {
		return err
	}
	out, err := s.watchItems(r, ws, items)
	if err != nil {
		return err
	}
	httpx.JSON(w, r, 200, map[string]any{"items": out})
	return nil
}

func (s *Server) addWatch(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		EntityID uuid.UUID         `json:"entity_id"`
		Tags     []string          `json:"tags"`
		Note     string            `json:"note"`
		Alerts   []watchlist.Alert `json:"alerts"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	_, ws := principal(r)
	it, err := s.Watch.Add(r.Context(), ws, in.EntityID, in.Tags, in.Note, in.Alerts)
	if err != nil {
		return err
	}
	out, err := s.watchItems(r, ws, []watchlist.Item{it})
	if err != nil {
		return err
	}
	httpx.JSON(w, r, http.StatusCreated, out[0])
	return nil
}

func (s *Server) patchWatch(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "item_id")
	if err != nil {
		return err
	}
	var in struct {
		Tags     *[]string          `json:"tags"`
		Note     *string            `json:"note"`
		Alerts   *[]watchlist.Alert `json:"alerts"`
		Expected int64              `json:"expected_version"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	_, ws := principal(r)
	it, err := s.Watch.Update(r.Context(), ws, id, in.Expected, in.Tags, in.Note, in.Alerts)
	if err != nil {
		return err
	}
	out, err := s.watchItems(r, ws, []watchlist.Item{it})
	if err != nil {
		return err
	}
	httpx.JSON(w, r, 200, out[0])
	return nil
}

func (s *Server) deleteWatch(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "item_id")
	if err != nil {
		return err
	}
	_, ws := principal(r)
	if err := s.Watch.Remove(r.Context(), ws, id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
