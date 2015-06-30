.PHONY: all test cover install

all: test

test:
	go test

cover:
	go test -cover -coverprofile=coverage.out
	go tool cover -html=coverage.out

install:
	go install
