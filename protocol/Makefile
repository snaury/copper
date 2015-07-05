.PHONY: all install

all: install

copper.pb.go: copper.proto
	protoc --go_out=. $<

install: copper.pb.go
	go install
