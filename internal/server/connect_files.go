package server

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
)

type fileServiceHandler struct {
	api *API
}

func (api *API) registerFileService() {
	serviceDesc := ariadnev1.File_ariadne_v1_files_proto.Services().ByName("FileService")
	handler := &fileServiceHandler{api: api}
	api.mountConnect(serviceDesc, func(opts ...connect.HandlerOption) (string, http.Handler) {
		return ariadnev1connect.NewFileServiceHandler(handler, opts...)
	})
}

func (h *fileServiceHandler) GetFileSheet(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetFileSheetRequest],
) (*connect.Response[ariadnev1.GetFileSheetResponse], error) {
	id := req.Msg.GetFileId()
	if !fileID.MatchString(id) {
		return nil, domainerr.NotFound(id)
	}
	index := int(req.Msg.GetIndex())
	if index < 0 {
		return nil, badInput("invalid worksheet index")
	}
	preview, err := h.api.files.PreviewSheet(ctx, id, index)
	if err != nil {
		return nil, err
	}
	rows := make([]*ariadnev1.FileSheetRow, len(preview.Rows))
	for i, cells := range preview.Rows {
		rows[i] = &ariadnev1.FileSheetRow{Cells: cells}
	}
	rowNumbers := make([]int64, len(preview.RowNumbers))
	for i, number := range preview.RowNumbers {
		rowNumbers[i] = int64(number)
	}
	return connect.NewResponse(&ariadnev1.GetFileSheetResponse{
		Sheets:     preview.Sheets,
		Rows:       rows,
		RowNumbers: rowNumbers,
		Truncated:  preview.Truncated,
	}), nil
}
