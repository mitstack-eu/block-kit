# syntax=docker/dockerfile:1.7
# Builds the operator image from a copy of this example whose go.mod has no replace line.
FROM golang:1.26.10-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS build
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
