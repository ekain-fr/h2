package version

import (
	"regexp"
	"testing"
)

func TestVersionIsSemver(t *testing.T) {
	// Simplified semver regex: MAJOR.MINOR.PATCH with optional pre-release
	semverRe := regexp.MustCompile(`^\d+\.\d+\.\d+(-[a-zA-Z0-9.]+)?$`)
	if !semverRe.MatchString(Version) {
		t.Errorf("Version %q is not a valid semver string", Version)
	}
}
