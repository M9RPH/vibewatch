.PHONY: test test-integration test-netem build run stop logs clean

test:
	go test ./...

test-integration:
	./scripts/test-integration.sh

test-netem:
	./scripts/test-netem.sh

build:
	docker compose build

run:
	docker compose up -d --build

stop:
	docker compose down

logs:
	docker compose logs -f vibewatch

clean:
	docker compose down --remove-orphans
