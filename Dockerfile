# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/dogecloud-webdav-gateway ./cmd/dogecloud-webdav-gateway

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/dogecloud-webdav-gateway /usr/local/bin/dogecloud-webdav-gateway

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/dogecloud-webdav-gateway"]
