//go:build !linux

package main

import "github.com/swysgh/mill-box/option"

func runInUserNamespaceIfNeeded(options option.Options, optionsList []*OptionsEntry) error {
	return nil
}
