.PHONY: build test run vet fmt docker

build:
	go build -o bin/omnihook ./cmd/omnihook

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l .

run:
	go run ./cmd/omnihook up

docker:
	docker build -t omnihook:dev .
