.PHONY: all install

COPPER_GO := copper.pb.go

all: $(COPPER_GO) $(COPPER_PY) install

$(COPPER_GO): copper.proto
	protoc --go_out=$(dir $@) $<

install: $(COPPER_GO)
	go install
