//go:build !darwin && !linux && !windows

package notify

func dispatch(title, body string) error { return nil }
