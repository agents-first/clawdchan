//go:build !darwin && !linux

package notify

func dispatch(title, body string) error { return nil }
