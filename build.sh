#!/usr/bin/env bash

if [[ -z "$1" ]]; then
  PROGVER="$(git describe --tag)"
else
  PROGVER="$1"
fi

CURRENT_DIR=$(pwd)
OUT_DIR=${CURRENT_DIR}/out

PROGNAME="librespeed-cli"
DEFS_PATH="github.com/librespeed/speedtest-cli"
BINARY=${PROGNAME}-$(go env GOOS)-$(go env GOARCH)
BUILD_DATE=$(date -u "+%Y-%m-%d %H:%M:%S %Z")
LDFLAGS="-w -s -X \"${DEFS_PATH}/defs.ProgName=${PROGNAME}\" -X \"${DEFS_PATH}/defs.ProgVersion=${PROGVER}\" -X \"${DEFS_PATH}/defs.BuildDate=${BUILD_DATE}\""

if [[ -n "${GOARM}" ]] && [[ "${GOARM}" -gt 0 ]]; then
  BINARY=${BINARY}v${GOARM}
fi

if [[ -n "${GOMIPS}" ]]; then
  BINARY=${BINARY}-${GOMIPS}
fi

if [[ -n "${GOMIPS64}" ]]; then
  BINARY=${BINARY}-${GOMIPS64}
fi

if [[ "$(go env GOOS)" = "windows" ]]; then
  BINARY=${BINARY}.exe
fi

if [[ ! -d ${OUT_DIR} ]]; then
  mkdir "${OUT_DIR}"
fi

if [[ -e ${OUT_DIR}/${BINARY} ]]; then
  rm -f "${OUT_DIR}/${BINARY}"
fi

go build -o "${OUT_DIR}/${BINARY}" -ldflags "${LDFLAGS}" -trimpath main.go

if [[ ! $(go env GOARCH) == mips64* ]] && [[ -x $(command -v upx) ]]; then
  upx -qqq -9 "${OUT_DIR}/${BINARY}"
fi
