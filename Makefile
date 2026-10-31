VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null | sed "s/^v//" || true)
ifeq ($(VERSION),)
VERSION := 1.0.0
endif
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build dev test vet release docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o buggy ./cmd/buggy

dev: build
	./buggy serve --dev

test:
	go test ./...

vet:
	go vet ./...

release:
	@mkdir -p dist
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 freebsd/amd64; do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  echo "building $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/buggy-$$os-$$arch$$ext ./cmd/buggy || exit 1; \
	done

docker:
	docker build --build-arg VERSION=$(VERSION) -t buggy:$(VERSION) -t buggy:latest .

clean:
	rm -rf buggy dist
