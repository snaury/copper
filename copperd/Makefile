.PHONY: all test cover

all: test

test:
	go test

cover:
	go test -cover -coverprofile=coverage.out
	go tool cover -html=coverage.out
