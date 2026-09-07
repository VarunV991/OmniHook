FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Schema is embedded in the binary (internal/db mirrors migrations/ by CI gate).
RUN CGO_ENABLED=0 go build -o /omnihook ./cmd/omnihook

FROM gcr.io/distroless/static:nonroot
COPY --from=build /omnihook /omnihook
VOLUME ["/data"]
# BIND must be 0.0.0.0 here: container loopback is not the host's loopback.
# Gate the exposed UI/API with ACCESS_TOKEN (compose sets a placeholder).
ENV BIND=0.0.0.0 PORT=8080 DATA_DIR=/data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/omnihook", "up"]
