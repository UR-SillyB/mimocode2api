package model

import "time"

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