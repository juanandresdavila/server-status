BINARY := server-status
LINUX  := dist/$(BINARY)-linux-amd64
# Diagnóstico temporal: mide el egress por v4 y por v6 en paralelo.
EGRESS := dist/egress-probe-linux-amd64

.PHONY: test vet build linux egress-linux egress-deploy deploy hooks

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

# El pinger del experimento de egress. Va al VPS como unit APARTE: no toca la
# unit ni la base de server-status, que además es el control positivo de la
# medición. Se saca cuando el experimento termina.
egress-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(EGRESS) ./cmd/egress-probe

egress-deploy: egress-linux
	scp $(EGRESS) vps:/tmp/egress-probe
	scp deploy/egress-probe.service vps:/tmp/egress-probe.service
	ssh vps 'sudo install -m 0755 /tmp/egress-probe /usr/local/bin/egress-probe && \
	         sudo install -m 0644 /tmp/egress-probe.service /etc/systemd/system/egress-probe.service && \
	         sudo systemctl daemon-reload && sudo systemctl enable --now egress-probe && \
	         rm -f /tmp/egress-probe /tmp/egress-probe.service'

deploy: linux
	scp $(LINUX) vps:/tmp/server-status
	ssh vps 'sudo install -m 0755 /tmp/server-status /usr/local/bin/server-status && sudo systemctl restart server-status && rm -f /tmp/server-status'

hooks:
	git config core.hooksPath .githooks
