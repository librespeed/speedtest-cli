FROM golang:1.20.3-alpine as builder

RUN apk add --no-cache bash upx

# Set working directory
WORKDIR /usr/src/librespeed-cli

# Copy librespeed-cli
COPY . .

# Build librespeed-cli
RUN ./build.sh

FROM alpine:3.17

# Copy librespeed-cli binary
COPY --from=builder /usr/src/librespeed-cli/out/librespeed-cli* /bin/librespeed-cli

CMD ["/bin/librespeed-cli"]
