package config

import (
	"fmt"
	"regexp"
)

var podNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// ValidatePodName checks that a pod name matches [a-z0-9-]+.
func ValidatePodName(name string) error {
	if !podNameRe.MatchString(name) {
		return fmt.Errorf("invalid pod name %q: must match [a-z0-9-]+", name)
	}
	return nil
}
