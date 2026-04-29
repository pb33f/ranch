// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package store

// StoreContentResponse contains a complete store snapshot for sync clients.
type StoreContentResponse struct {
	Items        map[string]any `json:"items"`
	ResponseType string         `json:"responseType"` // should be "storeContentResponse"
	StoreId      string         `json:"storeId"`
	StoreVersion int64          `json:"storeVersion"`
}

// NewStoreContentResponse creates a complete store snapshot response.
func NewStoreContentResponse(
	storeId string, items map[string]any, storeVersion int64) *StoreContentResponse {

	return &StoreContentResponse{
		ResponseType: "storeContentResponse",
		StoreId:      storeId,
		Items:        items,
		StoreVersion: storeVersion,
	}
}

// UpdateStoreResponse contains a single item update for sync clients.
type UpdateStoreResponse struct {
	ItemId       string `json:"itemId"`
	NewItemValue any    `json:"newItemValue"`
	ResponseType string `json:"responseType"` // should be "updateStoreResponse"
	StoreId      string `json:"storeId"`
	StoreVersion int64  `json:"storeVersion"`
}

// NewUpdateStoreResponse creates a single item update response.
func NewUpdateStoreResponse(
	storeId string, itemId string, newValue any, storeVersion int64) *UpdateStoreResponse {

	return &UpdateStoreResponse{
		ResponseType: "updateStoreResponse",
		StoreId:      storeId,
		StoreVersion: storeVersion,
		ItemId:       itemId,
		NewItemValue: newValue,
	}
}
