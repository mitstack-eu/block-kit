# syntax=docker/dockerfile:1.7
# Builds the operator image from a copy of this example whose go.mod has no replace line.
FROM golang:1.26.11-alpine@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags="-s -w" -o /out/block .

FROM alpine:3.24
RUN apk upgrade --no-cache && adduser -D -u 10001 block
COPY --from=build /out/block /app/block
USER block
EXPOSE 8080
ENTRYPOINT ["/app/block"]
