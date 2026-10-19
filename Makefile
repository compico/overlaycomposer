# Все команды — тонкие обёртки над `go run ./cmd/build` (он работает одинаково на Windows и Linux).
# Нужны: Go 1.26+, Node.js (только чтобы поставить three.js при первой сборке).

.PHONY: help tidy dev client test windows linux all clean

help:
	@echo make tidy      - go mod tidy (первый раз: скачать зависимости и зафиксировать версии)
	@echo make dev       - собрать клиент и запустить из исходников в портативном режиме (.dev/)
	@echo make client    - только бандлы клиента (web/dist)
	@echo make test      - go vet + go test
	@echo make windows   - build/overlay-composer-windows-amd64.exe (без консоли, с иконкой)
	@echo make linux     - build/overlay-composer-linux-amd64
	@echo make all       - оба бинарника

tidy:
	go mod tidy

client:
	go run ./cmd/build client --dev

dev: client
	go run ./cmd/overlay-composer --home .dev --no-open

test:
	go vet ./...
	go test ./...
	node --experimental-strip-types web/shared/geometry.test.ts

windows:
	go run ./cmd/build bin windows

linux:
	go run ./cmd/build bin linux

all:
	go run ./cmd/build bin all

clean:
	go run ./cmd/build clean
