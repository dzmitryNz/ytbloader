.PHONY: build run clean

build:
	go build -o bin/ytbloader cmd/server/main.go

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