.PHONY: all install

all: install

copper.pb.go: copper.proto
	protoc --go_out=. $<

copper_pb2.py: copper.proto
	protoc --python_out=. $<

install: copper.pb.go copper_pb2.py
	go install
