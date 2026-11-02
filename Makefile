BIN := bin/harbor

.PHONY: build test vet lint fmt run docker ci

build:
	go build -o $(BIN) ./cmd/harbor

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

lint: vet
	golangci-lint run

fmt:
	gofmt -w .

run:
	go run ./cmd/harbor -config configs/harbor.example.yaml

docker:
	docker compose up --build

ci: vet test build
