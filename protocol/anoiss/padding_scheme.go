package anoiss

import (
	"fmt"
	"strconv"
	"strings"
)

// paddingSchemeLines follows sing-anytls' newline-separated settings format.
func paddingSchemeLines(scheme []string) []string {
	return strings.Split(strings.Join(scheme, "\n"), "\n")
}

func paddingSchemeStop(scheme []string) (int, error) {
	for _, line := range paddingSchemeLines(scheme) {
		key, value, found := strings.Cut(line, "=")
		if found && key == "stop" {
			stop, err := strconv.Atoi(value)
			if err != nil || stop < 0 {
				return 0, fmt.Errorf("anoiss: invalid padding_scheme: stop %q must be a non-negative integer", value)
			}
			return stop, nil
		}
	}
	return 0, fmt.Errorf("anoiss: invalid padding_scheme: missing stop")
}

func validatePaddingScheme(scheme []string) error {
	if _, err := paddingSchemeStop(scheme); err != nil {
		return err
	}
	for lineNumber, line := range paddingSchemeLines(scheme) {
		key, value, found := strings.Cut(line, "=")
		if !found {
			return fmt.Errorf("anoiss: invalid padding_scheme: line %d: expected key=value", lineNumber+1)
		}
		if key == "stop" {
			stop, err := strconv.Atoi(value)
			if err != nil || stop < 0 {
				return fmt.Errorf("anoiss: invalid padding_scheme: stop %q must be a non-negative integer", value)
			}
			continue
		}
		index, err := strconv.ParseUint(key, 10, 32)
		if err != nil || strconv.FormatUint(index, 10) != key {
			return fmt.Errorf("anoiss: invalid padding_scheme: line %d: invalid index %q", lineNumber+1, key)
		}
		validRanges := 0
		for _, item := range strings.Split(value, ",") {
			if item == "c" {
				continue
			}
			minText, maxText, found := strings.Cut(item, "-")
			if !found {
				return fmt.Errorf("anoiss: invalid padding_scheme: index %d: invalid range %q", index, item)
			}
			if maxText == "" {
				return fmt.Errorf("anoiss: invalid padding_scheme: index %d: range %q has no max", index, item)
			}
			minSize, minErr := strconv.Atoi(minText)
			maxSize, maxErr := strconv.Atoi(maxText)
			if minErr != nil || maxErr != nil || minSize <= 0 || minSize > maxSize || maxSize > 1<<20 {
				return fmt.Errorf("anoiss: invalid padding_scheme: index %d: invalid range %q (require 0 < min <= max <= %d)", index, item, 1<<20)
			}
			validRanges++
		}
		if validRanges == 0 {
			return fmt.Errorf("anoiss: invalid padding_scheme: index %d: no valid range", index)
		}
	}
	return nil
}
