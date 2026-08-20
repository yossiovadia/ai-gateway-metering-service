package handler

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/noyitz/ai-gateway-metering-service/internal/dashboard"
	"github.com/noyitz/ai-gateway-metering-service/internal/k8s"
)

type AdminHandler struct {
	k8sClient      *k8s.Client
	deploymentType string // "praxis" or "ipp"
}

func NewAdminHandler(k8sClient *k8s.Client) *AdminHandler {
	h := &AdminHandler{k8sClient: k8sClient, deploymentType: "ipp"}
	if k8sClient != nil {
		if _, err := k8sClient.ReadPraxisConfig(context.Background(), "praxis-config"); err == nil {
			h.deploymentType = "praxis"
			slog.Info("detected Praxis deployment — routing tab will read praxis-config")
		}
	}
	return h
}

func isAdmin(r *http.Request) bool {
	user := r.Header.Get("X-Forwarded-User")
	return user == "" || user == "admin" || user == "kube:admin"
}

func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get("X-Forwarded-User")
		slog.Info("RequireAdmin check", "path", r.URL.Path, "X-Forwarded-User", user, "isAdmin", isAdmin(r))
		if !isAdmin(r) {
			http.Redirect(w, r, "/me", http.StatusFound)
			return
		}
		next(w, r)
	}
}

func (h *AdminHandler) ServeAdmin(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(dashboard.FS, "admin.html")
	if err != nil {
		http.Error(w, "admin page not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (h *AdminHandler) ServeMyAccount(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(dashboard.FS, "myaccount.html")
	if err != nil {
		http.Error(w, "my account page not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (h *AdminHandler) ServeRouting(w http.ResponseWriter, r *http.Request) {
	page := "routing.html"
	if h.deploymentType == "praxis" {
		page = "pipeline.html"
	}
	data, err := fs.ReadFile(dashboard.FS, page)
	if err != nil {
		http.Error(w, "routing page not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (h *AdminHandler) ServeCompression(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(dashboard.FS, "compression.html")
	if err != nil {
		http.Error(w, "compression page not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (h *AdminHandler) HandleProviders(w http.ResponseWriter, r *http.Request) {
	if h.k8sClient == nil {
		writeJSON(w, []k8s.ProviderInfo{})
		return
	}
	if h.deploymentType == "praxis" {
		result, err := h.k8sClient.ReadPraxisConfig(r.Context(), "praxis-config")
		if err != nil {
			writeJSON(w, []k8s.ProviderInfo{})
			return
		}
		writeJSON(w, k8s.ProvidersFromPraxis(result.Config))
		return
	}
	providers, err := h.k8sClient.ListProviders(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if providers == nil {
		providers = []k8s.ProviderInfo{}
	}
	writeJSON(w, providers)
}

func (h *AdminHandler) HandleModels(w http.ResponseWriter, r *http.Request) {
	if h.k8sClient == nil {
		writeJSON(w, []k8s.ModelInfo{})
		return
	}
	if h.deploymentType == "praxis" {
		result, err := h.k8sClient.ReadPraxisConfig(r.Context(), "praxis-config")
		if err != nil {
			writeJSON(w, []k8s.ModelInfo{})
			return
		}
		writeJSON(w, k8s.ModelsFromPraxis(result.Config))
		return
	}
	models, err := h.k8sClient.ListModels(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if models == nil {
		models = []k8s.ModelInfo{}
	}
	writeJSON(w, models)
}

func (h *AdminHandler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	if h.k8sClient == nil {
		writeJSON(w, &k8s.IPPConfig{Profiles: []k8s.ProfileInfo{}, ActiveProfile: "default"})
		return
	}
	if h.deploymentType == "praxis" {
		result, err := h.k8sClient.ReadPraxisConfig(r.Context(), "praxis-config")
		if err != nil {
			writeJSON(w, &k8s.IPPConfig{Profiles: []k8s.ProfileInfo{}, ActiveProfile: "default"})
			return
		}
		writeJSON(w, k8s.PipelineFromPraxis(result.Config))
		return
	}
	config, err := h.k8sClient.GetIPPConfig(r.Context(), "openshift-ingress")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, config)
}

func (h *AdminHandler) ServePipelineUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html, err := fs.ReadFile(dashboard.FS, "pipeline.html")
	if err != nil {
		http.Error(w, "pipeline UI not embedded", http.StatusNotFound)
		return
	}
	w.Write(html)
}

func (h *AdminHandler) HandlePipeline(w http.ResponseWriter, r *http.Request) {
	if h.k8sClient == nil || h.deploymentType != "praxis" {
		http.Error(w, "pipeline introspection only available in Praxis deployments", http.StatusNotFound)
		return
	}
	result, err := h.k8sClient.ReadPraxisConfig(r.Context(), "praxis-config")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	dump := map[string]interface{}{
		"config_source":      "praxis-config (ConfigMap)",
		"configuration":      map[string]interface{}{"filter_chains": result.Config.FilterChains},
		"resolved_listeners": buildResolvedListeners(result.Config, result.RawChains),
		"filter_types":       k8s.FilterTypesFromPraxis(result.Config),
	}
	writeJSON(w, dump)
}

func buildResolvedListeners(cfg *k8s.PraxisConfig, rawChains []map[string]interface{}) []map[string]interface{} {
	var listeners []map[string]interface{}
	for _, l := range cfg.Listeners {
		filters := resolveFiltersForListener(cfg, l, rawChains)
		chains := make([]string, len(l.FilterChains))
		copy(chains, l.FilterChains)
		listeners = append(listeners, map[string]interface{}{
			"name":    l.Name,
			"chains":  chains,
			"filters": filters,
		})
	}
	return listeners
}

func resolveFiltersForListener(cfg *k8s.PraxisConfig, listener k8s.PraxisListener, rawChains []map[string]interface{}) []map[string]interface{} {
	var filters []map[string]interface{}
	idx := 0
	for _, chainName := range listener.FilterChains {
		for _, chain := range cfg.FilterChains {
			if chain.Name != chainName {
				continue
			}
			rawFilters := findRawFilters(rawChains, chainName)
			for ci, f := range chain.Filters {
				entry := map[string]interface{}{
					"filter":         f.Filter,
					"chain":          chainName,
					"chain_index":    ci,
					"pipeline_index": idx,
					"failure_mode":   "closed",
				}
				if ci < len(rawFilters) {
					entry["config"] = extractFilterConfig(rawFilters[ci])
				}
				filters = append(filters, entry)
				idx++
			}
		}
	}
	return filters
}

func findRawFilters(rawChains []map[string]interface{}, chainName string) []map[string]interface{} {
	for _, rc := range rawChains {
		name, _ := rc["name"].(string)
		if name != chainName {
			continue
		}
		rawFilters, _ := rc["filters"].([]interface{})
		var result []map[string]interface{}
		for _, rf := range rawFilters {
			if m, ok := rf.(map[string]interface{}); ok {
				result = append(result, m)
			}
		}
		return result
	}
	return nil
}

var structuralFields = map[string]bool{
	"filter": true, "name": true, "branch_chains": true,
	"conditions": true, "response_conditions": true, "failure_mode": true,
}

func extractFilterConfig(raw map[string]interface{}) map[string]interface{} {
	config := make(map[string]interface{})
	for k, v := range raw {
		if structuralFields[k] {
			continue
		}
		if v == nil {
			continue
		}
		config[k] = v
	}
	if len(config) == 0 {
		return nil
	}
	return config
}

func (h *AdminHandler) HandleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.deploymentType == "praxis" {
		http.Error(w, "route changes not supported in Praxis deployments", http.StatusNotImplemented)
		return
	}
	if h.k8sClient == nil {
		http.Error(w, "k8s client not available", http.StatusServiceUnavailable)
		return
	}

	// Path: /api/v1/admin/models/provider/{modelName}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 6 {
		http.Error(w, "invalid path — expected /api/v1/admin/models/provider/{name}", http.StatusBadRequest)
		return
	}
	modelName := parts[5]

	var body struct {
		ProviderName string `json:"providerName"`
		TargetModel  string `json:"targetModel"`
		APIFormat    string `json:"apiFormat"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if err := h.k8sClient.UpdateModelProvider(r.Context(), modelName, body.ProviderName, body.TargetModel, body.APIFormat); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

func (h *AdminHandler) HandleUpdateWeights(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.deploymentType == "praxis" {
		http.Error(w, "weight changes not supported in Praxis deployments", http.StatusNotImplemented)
		return
	}
	if h.k8sClient == nil {
		http.Error(w, "k8s client not available", http.StatusServiceUnavailable)
		return
	}

	// Extract model name from path: /api/v1/admin/models/{name}/weights
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	modelName := parts[4]

	var weights map[string]int64
	if err := json.NewDecoder(r.Body).Decode(&weights); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if err := h.k8sClient.UpdateModelWeights(r.Context(), modelName, weights); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}
