FROM golang:1.26-alpine AS builder

RUN apk add --no-cache bash upx git

# Set working directory
WORKDIR /usr/src/librespeed-cli

# Copy librespeed-cli
COPY . .

# Build librespeed-cli
RUN ./build.sh

FROM alpine:3.24

# Copy librespeed-cli binary
COPY --from=builder /usr/src/librespeed-cli/out/librespeed-cli* /bin/librespeed-cli

ENTRYPOINT ["/bin/librespeed-cli"]
