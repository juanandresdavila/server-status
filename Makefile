BINARY := server-status
LINUX  := dist/$(BINARY)-linux-amd64

.PHONY: test vet build linux deploy hooks

test:
	go test ./... -race

vet:
	go vet ./...

build:
	go build -o $(BINARY) ./cmd/server-status

# CGO_ENABLED=0 no es decorativo: es la invariante 7 del spec.
# Si alguna dependencia necesitara cgo, este build falla y ahí nos enteramos.
linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(LINUX) ./cmd/server-status

deploy: linux
	scp $(LINUX) vps:/tmp/server-status
	ssh vps 'sudo install -m 0755 /tmp/server-status /usr/local/bin/server-status && sudo systemctl restart server-status && rm -f /tmp/server-status'

hooks:
	git config core.hooksPath .githooks
