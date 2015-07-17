.PHONY: all install

COPPER_GO := copper.pb.go
COPPER_PY := ../contrib/python-copper/lib/copper/copper_pb2.py

all: $(COPPER_GO) $(COPPER_PY) install

$(COPPER_GO): copper.proto
	protoc --go_out=$(dir $@) $<

$(COPPER_PY): copper.proto
	protoc --python_out=$(dir $@) $<

install: $(COPPER_GO)
	go install
