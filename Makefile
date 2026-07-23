.PHONY: test run build-arm

export GOCACHE := $(CURDIR)/.cache/go-build
export GOMODCACHE := $(CURDIR)/.cache/go-mod
export GOPATH := $(CURDIR)/.cache/gopath

test:
	go test ./...

run:
	go run ./cmd/iptv-control

build-arm:
	GOOS=linux GOARCH=arm GOARM=5 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/iptv-control ./cmd/iptv-control
