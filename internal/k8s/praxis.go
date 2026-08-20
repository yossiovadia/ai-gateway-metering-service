package k8s

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type PraxisConfig struct {
	Listeners    []PraxisListener    `yaml:"listeners" json:"listeners"`
	FilterChains []PraxisFilterChain `yaml:"filter_chains" json:"filter_chains"`
}

type PraxisListener struct {
	Name         string   `yaml:"name" json:"name"`
	Address      string   `yaml:"address" json:"address"`
	FilterChains []string `yaml:"filter_chains" json:"filter_chains"`
}

type PraxisFilterChain struct {
	Name    string              `yaml:"name" json:"name"`
	Filters []PraxisFilterEntry `yaml:"filters" json:"filters"`
}

type PraxisFilterEntry struct {
	Filter   string          `yaml:"filter" json:"filter"`
	Clusters []PraxisCluster `yaml:"clusters,omitempty" json:"clusters,omitempty"`
	Routes   []PraxisRoute   `yaml:"routes,omitempty" json:"routes,omitempty"`
}

type PraxisRoute struct {
	PathPrefix string `yaml:"path_prefix" json:"path_prefix"`
	Cluster    string `yaml:"cluster" json:"cluster"`
}

type PraxisCluster struct {
	Name      string     `yaml:"name" json:"name"`
	TLS       *PraxisTLS `yaml:"tls,omitempty" json:"tls,omitempty"`
	Endpoints []string   `yaml:"endpoints" json:"endpoints"`
}

type PraxisTLS struct {
	SNI string `yaml:"sni,omitempty" json:"sni,omitempty"`
}

type PraxisConfigResult struct {
	Config    *PraxisConfig
	RawChains []map[string]interface{}
}

func (c *Client) ReadPraxisConfig(ctx context.Context, configMapName string) (*PraxisConfigResult, error) {
	cmGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	cm, err := c.client.Resource(cmGVR).Namespace(c.namespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get ConfigMap %s: %w", configMapName, err)
	}

	configYAML, found, _ := unstructured.NestedString(cm.Object, "data", "praxis.yaml")
	if !found {
		return nil, fmt.Errorf("praxis.yaml not found in ConfigMap %s", configMapName)
	}

	var cfg PraxisConfig
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		return nil, fmt.Errorf("parse praxis.yaml: %w", err)
	}

	// Parse raw filter chains as untyped maps to preserve all config fields
	var raw struct {
		FilterChains []map[string]interface{} `yaml:"filter_chains"`
	}
	yaml.Unmarshal([]byte(configYAML), &raw)

	return &PraxisConfigResult{Config: &cfg, RawChains: raw.FilterChains}, nil
}

func ProvidersFromPraxis(cfg *PraxisConfig) []ProviderInfo {
	seen := make(map[string]bool)
	var providers []ProviderInfo

	for _, chain := range cfg.FilterChains {
		for _, f := range chain.Filters {
			if f.Filter != "load_balancer" {
				continue
			}
			for _, cluster := range f.Clusters {
				if seen[cluster.Name] {
					continue
				}
				seen[cluster.Name] = true

				endpoint := ""
				if len(cluster.Endpoints) > 0 {
					endpoint = cluster.Endpoints[0]
				}

				sni := ""
				if cluster.TLS != nil {
					sni = cluster.TLS.SNI
				}

				providers = append(providers, ProviderInfo{
					Name:           cluster.Name,
					Provider:       cluster.Name,
					Endpoint:       endpoint,
					Phase:          "Active",
					AuthType:       authTypeForChain(chain),
					SecretName:     sni,
					HasCredentials: true,
				})
			}
		}
	}

	return providers
}

func PipelineFromPraxis(cfg *PraxisConfig) *IPPConfig {
	result := &IPPConfig{ActiveProfile: "default"}

	for _, chain := range cfg.FilterChains {
		profile := ProfileInfo{Name: chain.Name}
		for _, f := range chain.Filters {
			profile.RequestPlugins = append(profile.RequestPlugins, f.Filter)
		}
		result.Profiles = append(result.Profiles, profile)
		if result.ActiveProfile == "default" {
			result.ActiveProfile = chain.Name
		}
	}

	if result.Profiles == nil {
		result.Profiles = []ProfileInfo{}
	}

	return result
}

func ModelsFromPraxis(cfg *PraxisConfig) []ModelInfo {
	var models []ModelInfo

	for _, listener := range cfg.Listeners {
		if listener.Name == "benchmark" {
			continue
		}

		cluster := clusterForListener(cfg, listener)
		if cluster == "" {
			continue
		}

		apiFormat := inferAPIFormat(listener.Name)

		models = append(models, ModelInfo{
			Name:      fmt.Sprintf("%s (:%s)", listener.Name, portFromAddress(listener.Address)),
			Namespace: "praxis",
			ProviderRefs: []ProviderRef{{
				ProviderName: cluster,
				TargetModel:  "all models",
				APIFormat:    apiFormat,
				Weight:       1,
			}},
		})
	}

	return models
}

type FilterTypeInfo struct {
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
}

var defaultDescriptions = map[string]FilterTypeInfo{
	"api_key_auth":          {Description: "Validates API keys against an external key management service via HTTP callout. Extracts authenticated identity into filter metadata. Caches validated keys in-memory with configurable TTL.", Category: "auth"},
	"identity_header_guard": {Description: "Captures request headers matching a configurable prefix into filter metadata and strips them before upstream forwarding. Prevents identity headers from leaking to LLM providers.", Category: "security"},
	"router":                {Description: "Matches the request path against configured route rules and selects the upstream cluster.", Category: "routing"},
	"external_metering":     {Description: "Reports token usage to an external metering service via CloudEvents. Reads identity from filter metadata for per-user attribution. Fire-and-forget — never blocks the response path.", Category: "metering"},
	"token_count":           {Description: "Extracts token usage from provider response bodies — streaming SSE and non-streaming JSON. Supports OpenAI, Anthropic, Google, Bedrock, and Azure formats.", Category: "metering"},
	"token_usage_headers":   {Description: "Injects Praxis-Token-Input/Output/Total response headers from token count metadata.", Category: "metering"},
	"credential_injection":  {Description: "Strips the client API key and injects the real provider credential. The security boundary — users never see real provider keys.", Category: "auth"},
	"headers":               {Description: "Sets static request headers before forwarding to the upstream provider.", Category: "protocol"},
	"load_balancer":         {Description: "Selects an endpoint from the configured upstream cluster. Handles TLS, connection pooling, and health checking.", Category: "routing"},
	"jwt_auth":              {Description: "Validates JWT bearer tokens against a JWKS endpoint. Extracts configured claims into filter metadata.", Category: "auth"},
}

func FilterTypesFromPraxis(cfg *PraxisConfig) map[string]FilterTypeInfo {
	types := make(map[string]FilterTypeInfo)
	for _, chain := range cfg.FilterChains {
		for _, f := range chain.Filters {
			if _, exists := types[f.Filter]; exists {
				continue
			}
			if info, ok := defaultDescriptions[f.Filter]; ok {
				types[f.Filter] = info
			} else {
				types[f.Filter] = FilterTypeInfo{}
			}
		}
	}
	return types
}

func authTypeForChain(chain PraxisFilterChain) string {
	for _, f := range chain.Filters {
		switch f.Filter {
		case "api_key_auth":
			return "api-key"
		case "jwt_auth":
			return "jwt"
		}
	}
	return "none"
}

func clusterForListener(cfg *PraxisConfig, listener PraxisListener) string {
	for _, chainName := range listener.FilterChains {
		for _, chain := range cfg.FilterChains {
			if chain.Name != chainName {
				continue
			}
			for _, f := range chain.Filters {
				if f.Filter == "load_balancer" && len(f.Clusters) > 0 {
					return f.Clusters[0].Name
				}
			}
		}
	}
	return ""
}

func inferAPIFormat(listenerName string) string {
	switch strings.ToLower(listenerName) {
	case "anthropic":
		return "messages"
	case "openai":
		return "openai-chat"
	default:
		return "unknown"
	}
}

func portFromAddress(addr string) string {
	parts := strings.Split(addr, ":")
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	return addr
}
