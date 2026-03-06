.PHONY: build test lint fmt vet clean

build:
	go build -o bin/symphony ./cmd/symphony

test:
	go test -race -count=1 ./...

lint: vet fmt

vet:
	go vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -rf bin/
