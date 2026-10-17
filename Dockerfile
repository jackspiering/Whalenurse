# syntax=docker/dockerfile:1

FROM golang:1.27.2 AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/whalenurse ./cmd/whalenurse
# The state store lives in /data. A fresh named volume copies the ownership of the image's directory, so make it
# the nonroot user's; otherwise the volume is root-owned and the store cannot open.
RUN mkdir -p /out/data

# distroless/static has no shell, so the healthcheck below calls the binary.
# The image runs as uid 65532, which cannot read the Docker socket: give it the
# socket's group at run time with `group_add` (see docs/running.md).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/whalenurse /whalenurse
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME ["/data"]
USER 65532:65532
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 CMD ["/whalenurse", "healthcheck"]
ENTRYPOINT ["/whalenurse"]
CMD ["run"]
