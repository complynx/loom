package api

import (
	"net/http"

	"github.com/complynx/loom/internal/domain"
)

func (s *server) monthlyFunding(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.MonthlyFunding(r.Context(), p, r.PathValue("id"))
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
func (s *server) updateMonthlyFunding(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.MonthlyFundingPolicy
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.store.UpdateMonthlyFunding(r.Context(), p, r.PathValue("id"), body)
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}

func (s *server) monthlyFundingDefaults(w http.ResponseWriter, r *http.Request, _ domain.Principal) error {
	settings, err := s.store.GetSettings(r.Context())
	if err == nil {
		write(
			w,
			http.StatusOK,
			domain.MonthlyFundingConfiguration{
				Defaults: domain.FundingDefaults(settings.MonthlyFunding),
				Version:  settings.Version,
			},
		)
	}
	return err
}
func (s *server) updateMonthlyFundingDefaults(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.MonthlyFundingConfiguration
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.store.UpdateMonthlyFundingDefaults(r.Context(), p, body)
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}
