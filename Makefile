.PHONY: test build run stop logs clean

test:
	go test ./...

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
