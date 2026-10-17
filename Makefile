.PHONY: build run dev clean

build:
	go build -o bin/ytbloader cmd/server/main.go

dev:
	air

run: build
	./bin/ytbloader

clean:
	rm -rf bin/
	rm -rf downloads/

deps:
	go mod tidy

test:
	go test -v ./...

lint:
	golangci-lint run