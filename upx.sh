#!/bin/sh
(which upx > /dev/null && ls -1 dist/*/* | xargs -I{} -n1 -P 4 $(which upx) -9 "{}") || echo "not using upx for binary compression"
