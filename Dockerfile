# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/harbor ./cmd/harbor

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget \
    && adduser -D -H -u 65532 harbor
COPY --from=build /out/harbor /usr/local/bin/harbor
COPY configs/harbor.example.yaml /etc/harbor/config.yaml
USER harbor
EXPOSE 8080 8081
ENTRYPOINT ["harbor"]
CMD ["-config", "/etc/harbor/config.yaml"]
