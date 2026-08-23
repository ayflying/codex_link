//go:build !windows

package main

func acquireInstance(_, _ string) (func(), error) {
	return func() {}, nil
}
