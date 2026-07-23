.PHONY: test run build-arm

test:
	go test ./...

run:
	go run ./cmd/iptv-control

build-arm:
	GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/iptv-control ./cmd/iptv-control
