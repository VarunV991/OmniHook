.PHONY: build test verify run vet fmt fmt-check checkdocs docker

build:
	go build -o bin/omnihook ./cmd/omnihook

test:
	go test ./...

# Full regression gate: formatting, vet, tests, build, docs-freshness.
verify: fmt-check vet test build checkdocs
	@echo "VERIFY_OK"

vet:
	go vet ./...

fmt:
	gofmt -l .

fmt-check:
	test -z "$$(gofmt -l .)" || (echo "gofmt dirty:"; gofmt -l .; exit 1)

checkdocs:
	go run ./scripts/checkdocs

run:
	go run ./cmd/omnihook up

docker:
	docker build -t omnihook:dev .
