.PHONY: test test-integration test-netem pull run build run-local stop logs clean

test:
	go test ./...

test-integration:
	./scripts/test-integration.sh

test-netem:
	./scripts/test-netem.sh

# Official installation path: consume the published GHCR image.
pull:
	docker compose pull

run:
	docker compose up -d

# Developer/source build path. The base compose intentionally has no build key.
build:
	docker compose -f docker-compose.yml -f docker-compose.build.yml build

run-local:
	docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build

stop:
	docker compose down

logs:
	docker compose logs -f vibewatch

clean:
	docker compose down --remove-orphans
