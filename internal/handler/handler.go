package handler

import (
	"encoding/json"
	"net/http"

	"github.com/Sliverkiss/mimocode2api/internal/config"
	"github.com/Sliverkiss/mimocode2api/internal/model"
	"github.com/Sliverkiss/mimocode2api/internal/proxy"
)

type ProxyConfig struct {
	ChatURL      string
	BootstrapURL string
	Fingerprint  string
}

func Models(cfg *config.Config) http.HandlerFunc {
	var models model.ModelListResponse
	if len(cfg.ModelAliases) > 0 {
		models = model.ModelsWithAliases(cfg.ModelAliases)
	} else {
		models = model.DefaultModels()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models)
	}
}

func ChatCompletions(cfg ProxyConfig) http.HandlerFunc {
	h := proxy.ProxyHandler(cfg.ChatURL, cfg.BootstrapURL, cfg.Fingerprint)
	return func(w http.ResponseWriter, r *http.Request) {
		h(w, r)
	}
}

func Health() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}