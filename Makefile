build:
	CURRENT_DATETIME=$(shell date '+%Y-%m-%dT%H:%M:%S') && \
	CGO_ENABLED=0 go build -ldflags "-X main.BuildTime=$$CURRENT_DATETIME" -o collagify-tg ./cmd

docker:
	docker build -t collagify-tg .
