//go:build !windows

package main

import "errors"

func openBrowser(string) error {
	return errors.New("automatic browser launch is only enabled in the Windows desktop build")
}
