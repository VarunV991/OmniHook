FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /omnihook ./cmd/omnihook

FROM gcr.io/distroless/static:nonroot
COPY --from=build /omnihook /omnihook
COPY --from=build /src/migrations /migrations
VOLUME ["/data"]
ENV PORT=8080 DATA_DIR=/data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/omnihook", "up"]
