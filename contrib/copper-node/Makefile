all: build

.PHONY: all clean build install

BUILDDIR := $(PWD)/build

clean:
	rm -rf $(BUILDDIR)

build:
	mkdir -p $(BUILDDIR)
	cd ../../cmd/copper-node && go build -o $(BUILDDIR)/copper-node

install:
	install -d $(DESTDIR)/usr/bin
	install $(BUILDDIR)/copper-node $(DESTDIR)/usr/bin
