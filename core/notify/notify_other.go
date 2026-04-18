//go:build !darwin && !linux && !windows

package notify

func dispatch(m Message) error { return nil }
