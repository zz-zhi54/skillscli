#!/usr/bin/env bash
# 从仓库根目录运行；只向空输出目录写入，不删除或覆盖已有产物。
set -euo pipefail

out=${1:-dist}
mkdir -p "$out"
if [[ -n $(ls -A "$out") ]]; then
  echo "::error::Build output directory must be empty: $out" >&2
  exit 1
fi

assets=()
for target in darwin/arm64 windows/amd64 windows/arm64 linux/amd64 linux/arm64; do
  goos=${target%/*}
  goarch=${target#*/}
  name="skillscli_${goos}_${goarch}"
  if [[ $goos == windows ]]; then
    name+=.exe
  fi
  binary="$out/$name"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -o "$binary" .
  test -s "$binary"
  metadata=$(go version -m "$binary")
  [[ $metadata == *$'\tbuild\tGOOS='"$goos"$'\n'* ]]
  [[ $metadata == *$'\tbuild\tGOARCH='"$goarch"$'\n'* ]]
  [[ $metadata == *$'\tbuild\tCGO_ENABLED=0'* ]]
  file "$binary"
  if [[ $goos == "$(go env GOOS)" && $goarch == "$(go env GOARCH)" ]]; then
    "$binary" --help
  fi
  assets+=("$name")
done

(
  cd "$out"
  shasum -a 256 "${assets[@]}" > checksums.txt
  [[ $(wc -l < checksums.txt) -eq 5 ]]
  shasum -a 256 -c checksums.txt
)
