package server

import (
	"errors"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/update"
)

func updateDomainError(err error) error {
	if errors.Is(err, update.ErrBusy) {
		return domainerr.New("updates.error.busy", nil)
	}
	if errors.Is(err, update.ErrUnavailable) {
		return domainerr.New("updates.error.unavailable", nil)
	}
	return err
}

func (api *API) registerUpdates() {
	api.registerUpdatesRPC()
}
