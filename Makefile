.PHONY: build test race cover bench run tidy

build:
	go build -o glider.exe ./cmd/glider

test:
	go test ./...

race:
	go test -race ./...

cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out

bench:
	go test ./bench/... -bench=. -benchtime=2s

run: build
	./glider.exe --config configs/glider.yaml

tidy:
	go mod tidy
