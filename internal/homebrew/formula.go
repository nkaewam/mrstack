package homebrew

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

const archivePrefix = "mrstack_"

var (
	checksumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	versionPattern  = regexp.MustCompile(`(?m)^  version "[^"]+"$`)
	releasePattern  = regexp.MustCompile(`(?m)^(\s+url ")https://github\.com/nkaewam/mrstack/releases/download/[^"]+/mrstack_[^"]+_(darwin|linux)_(amd64|arm64)\.tar\.gz"(\n\s+sha256 ")[0-9a-f]{64}"$`)
)

// UpdateFormula updates the release version, archive URLs, and checksums in a
// mrstack Homebrew formula while preserving the rest of the formula.
func UpdateFormula(formula []byte, tag string, checksums []byte) ([]byte, error) {
	if !strings.HasPrefix(tag, "v") || len(tag) == 1 {
		return nil, fmt.Errorf("release tag %q must start with v", tag)
	}

	version := strings.TrimPrefix(tag, "v")
	sums, err := parseChecksums(checksums)
	if err != nil {
		return nil, err
	}

	if matches := versionPattern.FindAll(formula, -1); len(matches) != 1 {
		return nil, fmt.Errorf("expected one version declaration, found %d", len(matches))
	}

	var replacementErr error
	replacements := 0
	updated := releasePattern.ReplaceAllFunc(formula, func(block []byte) []byte {
		match := releasePattern.FindSubmatch(block)
		goos, goarch := string(match[2]), string(match[3])
		archive := fmt.Sprintf("%s%s_%s_%s.tar.gz", archivePrefix, version, goos, goarch)
		checksum, ok := sums[archive]
		if !ok {
			replacementErr = fmt.Errorf("checksum missing for %s", archive)
			return block
		}

		replacements++
		url := fmt.Sprintf(
			"https://github.com/nkaewam/mrstack/releases/download/%s/%s",
			tag,
			archive,
		)
		return []byte(string(match[1]) + url + `"` + string(match[4]) + checksum + `"`)
	})
	if replacementErr != nil {
		return nil, replacementErr
	}
	if replacements != 4 {
		return nil, fmt.Errorf("expected four release archive blocks, found %d", replacements)
	}

	updated = versionPattern.ReplaceAll(updated, []byte(fmt.Sprintf(`  version "%s"`, version)))
	return updated, nil
}

func parseChecksums(contents []byte) (map[string]string, error) {
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || !checksumPattern.MatchString(fields[0]) {
			return nil, fmt.Errorf("invalid checksum line %q", scanner.Text())
		}
		checksums[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	return checksums, nil
}
