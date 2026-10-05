package api

import (
	"hoanxu/internal/leaderboards"
	"net/http"
	"time"
)

func (s *Server) leaderboard(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "week"
	}
	snapshot, err := (&leaderboards.Service{Store: s.Store}).Read(r.Context(), period, time.Now(), "")
	s.reply(w, r, 200, snapshot.Board, err)
}
func (s *Server) myLeaderboard(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "week"
	}
	snapshot, err := (&leaderboards.Service{Store: s.Store}).Read(r.Context(), period, time.Now(), user(r).ID)
	if err != nil {
		s.reply(w, r, 0, nil, err)
		return
	}
	s.reply(w, r, 200, leaderboards.Personal{PeriodInfo: snapshot.PeriodInfo, Position: *snapshot.Me}, nil)
}
