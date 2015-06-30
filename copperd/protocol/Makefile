.PHONY: all install

all: install

copperd.pb.go: copperd.proto
	protoc --go_out=. $<

install: copperd.pb.go
	go install
