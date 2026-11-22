// Package domainerr preserves the localized error envelope consumed by the web UI.
package domainerr

import (
	"encoding/json"
	"errors"
	"net/http"
)

type Error struct {
	Key    string         `json:"key"`
	Params map[string]any `json:"params,omitempty"`

	// Field and Index give tool callers the location of an invalid reference.
	Field string `json:"-"`
	Index *int   `json:"-"`
}

func (err *Error) Error() string                   { return err.Key }
func New(key string, params map[string]any) *Error { return &Error{Key: key, Params: params} }
func NotFound(what string) *Error                  { return New("error.notFound", map[string]any{"what": what}) }

func NotFoundField(field, what string) *Error {
	err := NotFound(what)
	err.Field = field
	return err
}

func Write(writer http.ResponseWriter, err error) {
	var localized *Error
	status := http.StatusInternalServerError
	if errors.As(err, &localized) {
		status = http.StatusBadRequest
		switch localized.Key {
		case "error.notFound", "error.priceUnknown":
			status = http.StatusNotFound
		case "error.artifactConflict", "updates.error.busy", "updates.error.unavailable":
			status = http.StatusConflict
		case "error.modelCheck", "error.priceCatalog":
			status = http.StatusBadGateway
		}
	} else {
		localized = New("error.invalidBody", map[string]any{"reason": "internal"})
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": localized})
}
