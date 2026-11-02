//go:build !linux

package main

import "context"

func prepareSystemdUpgrade(context.Context, string, string) (bool, error) { return false, nil }
