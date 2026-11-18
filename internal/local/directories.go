package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
	"golang.org/x/text/collate"
)

func ListLocalDirectory(ctx context.Context, path string) (*sdk.DirectoryNames, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil
		}
		path = home
	}
	real, err := filepath.Abs(path)
	if err != nil {
		return nil, nil
	}
	directory, err := os.Open(real)
	if err != nil {
		return nil, nil
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	names := []string{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := directory.ReadDir(256)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, nil
		}
		for _, entry := range entries {
			if entry.IsDir() {
				names = append(names, entry.Name())
			} else if entry.Type()&os.ModeSymlink != 0 {
				if info, err := os.Stat(filepath.Join(real, entry.Name())); err == nil && info.IsDir() {
					names = append(names, entry.Name())
				}
			}
			if len(names) > 100000 {
				return nil, errors.New("directory picker entry count exceeded its limit")
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return &sdk.DirectoryNames{Path: real, Names: names}, nil
}
func BrowseLocal(ctx context.Context, typed string) (*sdk.DirectoryListing, error) {
	order := collate.New(systemLocale(), collate.Numeric, collate.IgnoreCase, collate.IgnoreDiacritics)
	return sdk.BrowseTyped(ctx, typed, ListLocalDirectory, sdk.BrowseOptions{Compare: order.CompareString, Lower: textutil.Lower, Trim: textutil.Trim})
}
