FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/traefik-cloudflare-manager ./src

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/traefik-cloudflare-manager /app/traefik-cloudflare-manager
EXPOSE 8080
CMD ["/app/traefik-cloudflare-manager"]
