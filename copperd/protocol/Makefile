all: install

install: copperd.pb.go
	go install

copperd.pb.go: copperd.proto
	protoc --go_out=. $<
