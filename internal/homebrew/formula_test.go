package homebrew

import (
	"strings"
	"testing"
)

func TestUpdateFormula(t *testing.T) {
	t.Parallel()

	formula := `class Mrstack < Formula
  desc "Keep this text"
  version "0.1.0"

  on_macos do
    on_arm do
      url "https://github.com/nkaewam/mrstack/releases/download/v0.1.0/mrstack_0.1.0_darwin_arm64.tar.gz"
      sha256 "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    end
    on_intel do
      url "https://github.com/nkaewam/mrstack/releases/download/v0.1.0/mrstack_0.1.0_darwin_amd64.tar.gz"
      sha256 "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/nkaewam/mrstack/releases/download/v0.1.0/mrstack_0.1.0_linux_arm64.tar.gz"
      sha256 "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
    end
    on_intel do
      url "https://github.com/nkaewam/mrstack/releases/download/v0.1.0/mrstack_0.1.0_linux_amd64.tar.gz"
      sha256 "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
    end
  end
end
`
	checksums := `1111111111111111111111111111111111111111111111111111111111111111  mrstack_0.1.1_darwin_amd64.tar.gz
2222222222222222222222222222222222222222222222222222222222222222  mrstack_0.1.1_darwin_arm64.tar.gz
3333333333333333333333333333333333333333333333333333333333333333  mrstack_0.1.1_linux_amd64.tar.gz
4444444444444444444444444444444444444444444444444444444444444444  mrstack_0.1.1_linux_arm64.tar.gz
`

	got, err := UpdateFormula([]byte(formula), "v0.1.1", []byte(checksums))
	if err != nil {
		t.Fatalf("UpdateFormula() error = %v", err)
	}

	for _, want := range []string{
		`version "0.1.1"`,
		`releases/download/v0.1.1/mrstack_0.1.1_darwin_amd64.tar.gz"`,
		`sha256 "1111111111111111111111111111111111111111111111111111111111111111"`,
		`releases/download/v0.1.1/mrstack_0.1.1_darwin_arm64.tar.gz"`,
		`sha256 "2222222222222222222222222222222222222222222222222222222222222222"`,
		`releases/download/v0.1.1/mrstack_0.1.1_linux_amd64.tar.gz"`,
		`sha256 "3333333333333333333333333333333333333333333333333333333333333333"`,
		`releases/download/v0.1.1/mrstack_0.1.1_linux_arm64.tar.gz"`,
		`sha256 "4444444444444444444444444444444444444444444444444444444444444444"`,
		`desc "Keep this text"`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("updated formula does not contain %q:\n%s", want, got)
		}
	}
}

func TestUpdateFormulaRejectsIncompleteChecksums(t *testing.T) {
	t.Parallel()

	formula := `  version "0.1.0"
      url "https://github.com/nkaewam/mrstack/releases/download/v0.1.0/mrstack_0.1.0_darwin_arm64.tar.gz"
      sha256 "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      url "https://github.com/nkaewam/mrstack/releases/download/v0.1.0/mrstack_0.1.0_darwin_amd64.tar.gz"
      sha256 "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
      url "https://github.com/nkaewam/mrstack/releases/download/v0.1.0/mrstack_0.1.0_linux_arm64.tar.gz"
      sha256 "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
      url "https://github.com/nkaewam/mrstack/releases/download/v0.1.0/mrstack_0.1.0_linux_amd64.tar.gz"
      sha256 "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
`
	checksums := `1111111111111111111111111111111111111111111111111111111111111111  mrstack_0.1.1_darwin_amd64.tar.gz`

	_, err := UpdateFormula([]byte(formula), "v0.1.1", []byte(checksums))
	if err == nil || !strings.Contains(err.Error(), "checksum missing") {
		t.Fatalf("UpdateFormula() error = %v, want missing checksum error", err)
	}
}

func TestUpdateFormulaRejectsBadTag(t *testing.T) {
	t.Parallel()

	if _, err := UpdateFormula([]byte("  version \"0.1.0\"\n"), "0.1.0", nil); err == nil {
		t.Fatal("UpdateFormula() expected error for tag without v prefix")
	}
}
