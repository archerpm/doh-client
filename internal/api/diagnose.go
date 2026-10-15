package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/archerpm/doh-client/internal/daemon"
)

func (s *Server) diagnose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("некорректный запрос диагностики: %w", err))
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeErr(w, http.StatusBadRequest, errors.New("после запроса диагностики есть лишние данные"))
		return
	}
	result, err := s.d.Diagnose(r.Context(), req.Domain)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, result)
}

func (c *Client) Diagnose(domain string) (r daemon.DiagnoseResult, err error) {
	err = c.do("POST", "/v1/diagnose", map[string]string{"domain": domain}, &r)
	return
}
