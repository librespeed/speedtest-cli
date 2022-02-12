FROM golang:1.17.7-buster as builder

# Set working directory
WORKDIR /usr/src/librespeed-cli

# Copy librespeed-cli
COPY . .

# Build librespeed-cli
RUN ./build.sh

FROM golang:1.17.7-buster

# Copy librespeed-cli binary
COPY --from=builder /usr/src/librespeed-cli/out/librespeed-cli* /usr/src/librespeed-cli/librespeed-cli

ENTRYPOINT ["/usr/src/librespeed-cli/librespeed-cli"]
