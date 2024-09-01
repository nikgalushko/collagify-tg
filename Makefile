build:
	CURRENT_DATETIME=$(shell date '+%Y-%m-%dT%H:%M:%S') && \
	CGO_ENABLED=1 go build -ldflags "-X main.BuildTime=$$CURRENT_DATETIME" -o collagify-tg ./cmd

docker:
	docker build --platform linux/amd64 -t jetuuuu/collagify-tg .

test:
	go test -v ./...
