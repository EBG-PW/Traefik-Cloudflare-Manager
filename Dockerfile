FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN mkdir -p /out/data \
    && chmod 0700 /out/data \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/traefik-cloudflare-manager ./src

FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/traefik-cloudflare-manager /app/traefik-cloudflare-manager
COPY --from=build --chown=65532:65532 /out/data /app/data
EXPOSE 8080
ENTRYPOINT ["/app/traefik-cloudflare-manager"]
