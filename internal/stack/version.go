package stack

import (
	"fmt"
	"regexp"
	"strconv"
)

var versionPattern = regexp.MustCompile(`^\s*(\d+)\.(\d+)(?:\.\d+)?(?:[-+].*)?\s*$`)

// SelectMode applies the 19.1 native-stack boundary. detectedVersion may be
// empty/unreadable only when an explicit valid mode is supplied.
func SelectMode(detectedVersion string, explicit Mode) (Mode, error) {
	if detectedVersion == "" {
		if explicit == ModeLegacy || explicit == ModeNative {
			return explicit, nil
		}
		return "", fmt.Errorf("%s: GitLab version is unreadable and --gitlab-mode is required", FindingInvalidArguments)
	}
	match := versionPattern.FindStringSubmatch(detectedVersion)
	if match == nil {
		return "", fmt.Errorf("%s: unrecognized GitLab version %q", FindingInvalidArguments, detectedVersion)
	}
	major, majorErr := strconv.Atoi(match[1])
	minor, minorErr := strconv.Atoi(match[2])
	if majorErr != nil || minorErr != nil {
		return "", fmt.Errorf("%s: GitLab version numbers are out of range", FindingInvalidArguments)
	}
	detected := ModeLegacy
	if major > 19 || major == 19 && minor >= 1 {
		detected = ModeNative
	}
	if explicit != "" && explicit != detected {
		return "", fmt.Errorf("%s: explicit mode %q contradicts detected mode %q", FindingInvalidArguments, explicit, detected)
	}
	if explicit != "" && explicit != ModeLegacy && explicit != ModeNative {
		return "", fmt.Errorf("%s: invalid explicit mode %q", FindingInvalidArguments, explicit)
	}
	return detected, nil
}
