//go:build !ariadne_web

package main

import "io/fs"

const bundledBuild = false

func embeddedWeb() (fs.FS, error) { return nil, nil }
