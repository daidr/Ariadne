package server

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/data"
	"github.com/daidr/Ariadne/internal/fsutil"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
)

type dataServiceHandler struct {
	api *API
}

func (api *API) registerData() {
	serviceDesc := ariadnev1.File_ariadne_v1_data_proto.Services().ByName("DataService")
	handler := &dataServiceHandler{api: api}
	api.mountConnect(serviceDesc, func(opts ...connect.HandlerOption) (string, http.Handler) {
		return ariadnev1connect.NewDataServiceHandler(handler, opts...)
	})
}

func (h *dataServiceHandler) GetDataUsage(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetDataUsageRequest],
) (*connect.Response[ariadnev1.GetDataUsageResponse], error) {
	store := h.api.options.Data
	if store == nil {
		return nil, badInput("data")
	}
	var (
		usage data.Usage
		err   error
	)
	if req.Msg.GetIncludeSizes() {
		usage, err = store.Usage(ctx)
	} else {
		usage, err = store.Summary()
	}
	if err != nil {
		return nil, err
	}
	backups := make([]*ariadnev1.BackupInfo, 0, len(usage.Backups))
	for _, item := range usage.Backups {
		backups = append(backups, toProtoBackupInfo(item))
	}
	return connect.NewResponse(&ariadnev1.GetDataUsageResponse{
		Home:       usage.Home,
		Database:   usage.Database,
		Plugins:    usage.Plugins,
		Backups:    backups,
		BackupsDir: usage.BackupsDir,
	}), nil
}

func (h *dataServiceHandler) GetStorageSnapshot(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetStorageSnapshotRequest],
) (*connect.Response[ariadnev1.GetStorageSnapshotResponse], error) {
	store := h.api.options.Data
	if store == nil {
		return nil, badInput("data")
	}
	if req.Msg.GetCachedOnly() {
		return connect.NewResponse(&ariadnev1.GetStorageSnapshotResponse{
			Storage: toProtoStorageSnapshot(store.LatestStorage()),
		}), nil
	}
	var paths []string
	if manager := h.api.options.Workspaces; manager != nil {
		projects, err := manager.ListWorktrees(ctx, false)
		if err != nil {
			return nil, err
		}
		for _, project := range projects {
			for _, tree := range project.Worktrees {
				if filepath.IsAbs(tree.Path) {
					paths = append(paths, tree.Path)
				}
			}
		}
	}
	snapshot, err := store.MeasureStorage(ctx, paths)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.GetStorageSnapshotResponse{
		Storage: toProtoStorageSnapshot(snapshot),
	}), nil
}

func (h *dataServiceHandler) MeasureWorktreeReclaimable(
	ctx context.Context,
	req *connect.Request[ariadnev1.MeasureWorktreeReclaimableRequest],
) (*connect.Response[ariadnev1.MeasureWorktreeReclaimableResponse], error) {
	manager := h.api.options.Workspaces
	path := req.Msg.GetPath()
	if manager == nil || !filepath.IsAbs(path) {
		return nil, badInput("path")
	}
	projects, err := manager.ListWorktrees(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		for _, tree := range project.Worktrees {
			if tree.Path == path {
				measureCtx, cancel := context.WithTimeout(ctx, time.Minute)
				defer cancel()
				return connect.NewResponse(&ariadnev1.MeasureWorktreeReclaimableResponse{
					Exclusive: fsutil.MeasureReFSExclusive(measureCtx, tree.Path),
				}), nil
			}
		}
	}
	return nil, badInput("path")
}

func (h *dataServiceHandler) CreateBackup(
	ctx context.Context,
	_ *connect.Request[ariadnev1.CreateBackupRequest],
) (*connect.Response[ariadnev1.CreateBackupResponse], error) {
	store := h.api.options.Data
	if store == nil {
		return nil, badInput("data")
	}
	backup, err := store.Backup(ctx, false, h.api.now())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.CreateBackupResponse{
		Backup: toProtoBackupInfo(backup),
	}), nil
}

func (h *dataServiceHandler) DeleteBackup(
	_ context.Context,
	req *connect.Request[ariadnev1.DeleteBackupRequest],
) (*connect.Response[ariadnev1.DeleteBackupResponse], error) {
	store := h.api.options.Data
	if store == nil {
		return nil, badInput("data")
	}
	if err := store.RemoveBackup(req.Msg.GetName()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.DeleteBackupResponse{}), nil
}

func toProtoBackupInfo(item data.BackupInfo) *ariadnev1.BackupInfo {
	return &ariadnev1.BackupInfo{
		Name:      item.Name,
		Size:      item.Size,
		CreatedAt: item.CreatedAt,
		Auto:      item.Auto,
	}
}

func toProtoStorageSnapshot(snapshot *data.StorageSnapshot) *ariadnev1.StorageSnapshot {
	if snapshot == nil {
		return nil
	}
	worktrees := make(map[string]*ariadnev1.DirectorySize, len(snapshot.Worktrees))
	for path, size := range snapshot.Worktrees {
		worktrees[path] = &ariadnev1.DirectorySize{
			Bytes:               size.Bytes,
			Exclusive:           size.Exclusive,
			ReclaimableEstimate: size.ReclaimableEstimate,
		}
	}
	return &ariadnev1.StorageSnapshot{
		Managed:    snapshot.Managed,
		Total:      snapshot.Total,
		Worktrees:  worktrees,
		Logical:    snapshot.Logical,
		MeasuredAt: snapshot.MeasuredAt,
	}
}
