#!/usr/bin/env bash
# Sube un archivo al VPS y VERIFICA que llegó entero.
#
# Existe por lo que pasó el 26/08/2026: una transferencia por el enlace de
# Tailscale se cortó a mitad y `scp` devolvió 0 igual. El binario quedó en
# 2,52 MB de 9,30 MB y segfaulteó al ejecutarse. Si eso le pasa a
# `server-status` en vez de a un diagnóstico, systemd queda en restart-loop y
# el monitoreo se muere justo cuando hace falta que avise.
#
#   deploy/subir.sh <archivo-local> <ruta-remota>
set -euo pipefail

local_f=${1:?falta el archivo local}
remoto=${2:?falta la ruta remota}
host=${VPS_HOST:-vps}

if command -v sha256sum >/dev/null; then
	esperado=$(sha256sum "$local_f" | cut -d' ' -f1)
else
	esperado=$(shasum -a 256 "$local_f" | awk '{print $1}')
fi

for intento in 1 2 3; do
	if ! scp -q "$local_f" "$host:$remoto"; then
		echo "subir.sh: scp falló (intento $intento)" >&2
		continue
	fi
	recibido=$(ssh "$host" "sha256sum '$remoto' | cut -d' ' -f1")
	if [ "$esperado" = "$recibido" ]; then
		exit 0
	fi
	echo "subir.sh: llegó truncado (intento $intento): $recibido != $esperado" >&2
done

echo "subir.sh: no se pudo subir $local_f íntegro en 3 intentos; NO se instaló nada" >&2
ssh "$host" "rm -f '$remoto'" || true
exit 1
