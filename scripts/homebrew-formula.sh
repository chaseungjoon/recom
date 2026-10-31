#!/bin/sh
# Render the Homebrew formula for a published release.
#   scripts/homebrew-formula.sh 0.1.0 checksums.txt > Formula/recom.rb
set -eu
version=${1:?usage: homebrew-formula.sh VERSION CHECKSUMS_FILE}
sums=${2:?usage: homebrew-formula.sh VERSION CHECKSUMS_FILE}
base="https://github.com/chaseungjoon/recom/releases/download/v${version}"

sha() {
	s=$(awk -v f="recom_${version}_$1.tar.gz" '$2 == f { print $1 }' "$sums")
	[ -n "$s" ] || { echo "no checksum for recom_${version}_$1.tar.gz" >&2; exit 1; }
	echo "$s"
}

# Resolve every checksum first: a failure inside a heredoc substitution
# would not stop the script.
darwin_arm64=$(sha darwin_arm64)
darwin_amd64=$(sha darwin_amd64)
linux_arm64=$(sha linux_arm64)
linux_amd64=$(sha linux_amd64)

cat <<EOF
class Recom < Formula
  desc "Run commands on a remote Linux box with your local project synced automatically"
  homepage "https://github.com/chaseungjoon/recom"
  version "${version}"
  license "MIT"

  on_macos do
    # macOS ships openrsync / rsync 2.6.9; recom needs rsync 3.x.
    depends_on "rsync"

    on_arm do
      url "${base}/recom_${version}_darwin_arm64.tar.gz"
      sha256 "${darwin_arm64}"
    end
    on_intel do
      url "${base}/recom_${version}_darwin_amd64.tar.gz"
      sha256 "${darwin_amd64}"
    end
  end

  on_linux do
    on_arm do
      url "${base}/recom_${version}_linux_arm64.tar.gz"
      sha256 "${linux_arm64}"
    end
    on_intel do
      url "${base}/recom_${version}_linux_amd64.tar.gz"
      sha256 "${linux_amd64}"
    end
  end

  def install
    bin.install "recom"
    generate_completions_from_executable(bin/"recom", "completion")
  end

  def caveats
    <<~EOS
      Register a backend (an SSH alias or user@host), then initialize a project:
        recom ssh aiworkstation
        cd ~/Projects/my-model && recom init
        recom python3 train.py
    EOS
  end

  test do
    assert_match "recom #{version}", shell_output("#{bin}/recom version")
    assert_match "linux/amd64, linux/arm64", shell_output("#{bin}/recom version")
  end
end
EOF
