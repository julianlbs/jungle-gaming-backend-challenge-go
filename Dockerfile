# syntax=docker/dockerfile:1

FROM golang:1.25.11-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/wallet ./cmd/wallet

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/wallet /wallet
USER nonroot:nonroot
EXPOSE 8080 9090
ENTRYPOINT ["/wallet"]
CMD ["serve"]
