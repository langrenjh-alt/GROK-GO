package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/langrenjh-alt/GROK-GO/internal/accountprobe"
	accountpool "github.com/langrenjh-alt/GROK-GO/internal/accounts"
)

const maximumProbeBatch = 100

type accountProbeRequest struct {
	Model string `json:"model"`
}

type accountBatchProbeRequest struct {
	IDs   []string `json:"ids"`
	Model string   `json:"model"`
}

func (h *Handler) probeAccount(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccountProbe(w) {
		return
	}
	var request accountProbeRequest
	if err := h.decodeJSON(w, r, &request); err != nil && !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	result, err := h.config.AccountProbe.Probe(r.Context(), chi.URLParam(r, "id"), accountprobe.Input{Model: request.Model})
	if err != nil {
		if errors.Is(err, accountpool.ErrAccountBusy) {
			writeAPIError(w, http.StatusConflict, "account_busy", err.Error())
			return
		}
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, result)
}

func (h *Handler) batchProbeAccounts(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccountProbe(w) {
		return
	}
	var request accountBatchProbeRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	request.IDs = uniqueStrings(request.IDs)
	if len(request.IDs) == 0 || len(request.IDs) > maximumProbeBatch {
		writeAPIError(w, http.StatusBadRequest, "invalid_probe_batch", "Select between 1 and 100 accounts.")
		return
	}
	result := h.config.AccountProbe.ProbeMany(r.Context(), request.IDs, accountprobe.Input{Model: request.Model})
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, result)
}

func (h *Handler) requireAccountProbe(w http.ResponseWriter) bool {
	if h.config.AccountProbe == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "account_probe_unavailable", "Account probing is not configured.")
		return false
	}
	return true
}
