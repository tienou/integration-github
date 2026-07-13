GO	= go
EXT	=

PLAKAR	= plakar
VERSION	= v0.1.0

all: build

build:
	CGO_ENABLED=0 ${GO} build -v -o github-importer${EXT} ./importer

package: build
	rm -f github_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm github

install: package
	${PLAKAR} pkg add ./github_${VERSION}_*.ptar

reinstall: uninstall install

clean:
	rm -f github-importer${EXT} github_${VERSION}*.ptar

.PHONY: all build package uninstall install reinstall clean
