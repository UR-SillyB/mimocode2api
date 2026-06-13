package model

import (
	"strings"
	"time"
)

// Static model list for the proxy
type ModelObject struct {
	ID              string `json:"id"`
	Object          string `json:"object"`
	Created         int64  `json:"created"`
	OwnedBy         string `json:"owned_by"`
	SupportsVision  bool   `json:"supports_vision"`
}

type ModelListResponse struct {
	Object string        `json:"object"`
	Data   []ModelObject `json:"data"`
}

func DefaultModels() ModelListResponse {
	now := time.Now().Unix()
	return ModelListResponse{
		Object: "list",
		Data: []ModelObject{
			{ID: "mimo-auto", Object: "model", Created: now, OwnedBy: "mimo-ai", SupportsVision: true},
		},
	}
}

// ModelsWithAliases returns the model list including client-recognized aliases.
// Aliases (like gpt-4o) are marked as vision-capable so clients enable image upload.
func ModelsWithAliases(aliases []string) ModelListResponse {
	now := time.Now().Unix()
	models := []ModelObject{
		{ID: "mimo-auto", Object: "model", Created: now, OwnedBy: "mimo-ai", SupportsVision: true},
	}
	for _, alias := range aliases {
		models = append(models, ModelObject{
			ID:             alias,
			Object:         "model",
			Created:        now,
			OwnedBy:        inferOwner(alias),
			SupportsVision: true,
		})
	}
	return ModelListResponse{Object: "list", Data: models}
}

func inferOwner(modelID string) string {
	if strings.HasPrefix(modelID, "gpt-") {
		return "openai"
	}
	if strings.HasPrefix(modelID, "claude-") {
		return "anthropic"
	}
	if strings.HasPrefix(modelID, "gemini-") {
		return "google"
	}
	return "mimo-ai"
}