package api

import (
	"github.com/go-chi/chi/v5"
	"hoanxu/internal/leaderboards"
	"net/http"
	"time"
)

func (s *Server) weeklyPrizeRoutes(r chi.Router) {
	svc := &leaderboards.PrizeService{Store: s.Store}
	r.Get("/admin/leaderboard-prizes", s.allowed("gifts", false, func(w http.ResponseWriter, r *http.Request) { v, e := svc.List(r.Context()); s.reply(w, r, 200, v, e) }))
	r.Post("/admin/leaderboard-prizes", s.allowed("gifts", true, func(w http.ResponseWriter, r *http.Request) {
		var p leaderboards.CampaignInput
		if !s.body(w, r, &p) {
			return
		}
		v, e := svc.Save(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), p, time.Now())
		s.reply(w, r, 200, v, e)
	}))
	r.Get("/admin/leaderboard-prizes/{id}/preview", s.allowed("gifts", false, func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !s.customerID(w, r, id) {
			return
		}
		v, e := svc.Preview(r.Context(), id, time.Now())
		s.reply(w, r, 200, v, e)
	}))
	r.Post("/admin/leaderboard-prizes/{id}/settle", s.allowed("gifts", true, func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !s.customerID(w, r, id) {
			return
		}
		var p struct {
			Hash string `json:"hash"`
		}
		if !s.body(w, r, &p) {
			return
		}
		v, e := svc.Settle(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), id, p.Hash, time.Now())
		s.reply(w, r, 200, v, e)
	}))
	r.Get("/admin/leaderboard-prizes/{id}/awards", s.allowed("gifts", false, func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !s.customerID(w, r, id) {
			return
		}
		v, e := svc.Awards(r.Context(), id, "")
		s.reply(w, r, 200, v, e)
	}))
	r.Post("/admin/leaderboard-awards/{id}/deliver", s.allowed("gifts", true, func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !s.customerID(w, r, id) {
			return
		}
		var p struct {
			DeliveryNote string `json:"deliveryNote"`
		}
		if !s.body(w, r, &p) {
			return
		}
		v, e := svc.Deliver(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), id, p.DeliveryNote)
		s.reply(w, r, 200, v, e)
	}))
}
func (s *Server) currentWeeklyPrize(w http.ResponseWriter, r *http.Request) {
	v, e := (&leaderboards.PrizeService{Store: s.Store}).Current(r.Context(), time.Now())
	s.reply(w, r, 200, v, e)
}
func (s *Server) myWeeklyAwards(w http.ResponseWriter, r *http.Request) {
	v, e := (&leaderboards.PrizeService{Store: s.Store}).Awards(r.Context(), "", user(r).ID)
	s.reply(w, r, 200, v, e)
}
