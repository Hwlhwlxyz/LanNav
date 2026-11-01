//go:build !windows && !linux

package listen

func tcp() ([]Port, error) { return nil, ErrUnsupported }
