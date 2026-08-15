#!/usr/bin/env bash
# Instala server-status en un host Linux con systemd. Correr como root desde
# la raíz del repo: sudo ./deploy/install.sh
#
# Idempotente: actualiza el binario y la unit, pero NUNCA pisa una config o un
# env existentes. No arranca el servicio — la config hay que completarla antes.
set -euo pipefail

BIN=/usr/local/bin/server-status
PRECOMPILADO=dist/server-status-linux-amd64

if [ "$(id -u)" -ne 0 ]; then
    echo "Correr como root: sudo ./deploy/install.sh" >&2
    exit 1
fi
if ! command -v systemctl >/dev/null; then
    echo "Este instalador necesita systemd." >&2
    exit 1
fi
if [ ! -f deploy/server-status.service ]; then
    echo "Correr desde la raíz del repo (no se encuentra deploy/)." >&2
    exit 1
fi

# --- binario -----------------------------------------------------------------
# O ya viene compilado (make linux en otra máquina), o se compila acá.
if [ -f "$PRECOMPILADO" ]; then
    echo "→ usando el binario precompilado $PRECOMPILADO"
    install -m 0755 "$PRECOMPILADO" "$BIN"
elif command -v go >/dev/null; then
    echo "→ compilando (CGO_ENABLED=0)"
    tmp=$(mktemp)
    CGO_ENABLED=0 go build -o "$tmp" ./cmd/server-status
    install -m 0755 "$tmp" "$BIN"
    rm -f "$tmp"
else
    echo "No hay ni Go ni $PRECOMPILADO." >&2
    echo "Opción A: instalar Go (https://go.dev/dl/) y volver a correr esto." >&2
    echo "Opción B: en otra máquina, 'make linux' y copiar dist/ acá." >&2
    exit 1
fi

# --- usuario -----------------------------------------------------------------
if ! id server-status >/dev/null 2>&1; then
    echo "→ creando el usuario de sistema server-status"
    useradd --system --no-create-home --shell /usr/sbin/nologin server-status
fi
# El socket de Docker es del grupo docker; sin Docker instalado no pasa nada.
if getent group docker >/dev/null; then
    usermod -aG docker server-status
fi

# --- directorios -------------------------------------------------------------
mkdir -p /etc/server-status
install -d -o server-status -g server-status /var/lib/server-status
# La portada la escribe el proceso y la sirve otro (Caddy, nginx...).
install -d -o server-status -g server-status /opt/status/public

# --- configuración (solo si no existe: acá viven valores editados a mano) ----
if [ ! -f /etc/server-status/config.yaml ]; then
    echo "→ copiando la config de ejemplo"
    cp deploy/config.example.yaml /etc/server-status/config.yaml
fi
if [ ! -f /etc/server-status/env ]; then
    echo "→ copiando el env de ejemplo (los secretos van acá, modo 0600)"
    cp deploy/env.example /etc/server-status/env
    chmod 0600 /etc/server-status/env
    chown server-status:server-status /etc/server-status/env
fi

# --- unit --------------------------------------------------------------------
cp deploy/server-status.service /etc/systemd/system/server-status.service
systemctl daemon-reload
systemctl enable server-status >/dev/null 2>&1

if systemctl is-active --quiet server-status; then
    echo "→ el servicio ya estaba corriendo: reiniciando con el binario nuevo"
    systemctl restart server-status
    echo
    echo "Listo. Ver cómo quedó: journalctl -u server-status -n 20 --no-pager"
else
    echo
    echo "Instalado. Antes de arrancar:"
    echo "  1. Editar /etc/server-status/config.yaml (servicios, panel, portada)"
    echo "  2. Editar /etc/server-status/env (tokens; ver INSTALLATION.md)"
    echo "  3. sudo systemctl start server-status"
    echo "  4. journalctl -u server-status -f"
fi
