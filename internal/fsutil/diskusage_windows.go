package fsutil

import "io/fs"

func diskEntry(info fs.FileInfo) (int64, fileIdentity) {
	// Windows directory enumeration already supplies the logical size. Using it
	// avoids opening every file and does not count junctions or symlink targets.
	if info.Mode().IsRegular() {
		return info.Size(), fileIdentity{}
	}
	return 0, fileIdentity{}
}
