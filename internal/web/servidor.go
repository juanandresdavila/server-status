// Package web sirve el panel privado y renderiza la portada pública.
//
// Las dos caras son deliberadamente asimétricas. El panel vive en el tailnet,
// lo atiende el proceso y muestra todo. La portada sale a internet, la sirve
// Caddy desde un archivo, y solo lleva lo que pasa por una lista blanca — el
// proceso le habla al socket de Docker, así que no puede además atender a
// internet.
package web

import (
	"embed"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Los assets y las plantillas viajan adentro del binario: el deploy sigue
// siendo copiar un solo archivo, que es la premisa del proyecto.
//
//go:embed assets
var assets embed.FS

//go:embed plantillas
var plantillas embed.FS

// Escuchar ata el panel a una dirección, reintentando mientras no exista.
//
// El panel escucha en la IP de tailnet, que al arrancar la máquina puede no
// estar asignada todavía: es la misma carrera que en este VPS se resolvió con
// FreeBind para Cockpit. Acá se resuelve reintentando, que es portable y no
// necesita build tags.
//
// El reintento TERMINA a propósito: uno infinito dejaría una goroutine viva
// para siempre contra una dirección mal configurada.
//
// Nunca es fatal para el proceso: quien llama lo hace en una goroutine y
// loguea. Un monitor sin panel sigue sirviendo; un monitor muerto no.
func Escuchar(direccion string, h http.Handler, plazo time.Duration) error {
	return EscucharComo("panel", direccion, h, plazo)
}

// EscucharComo es igual pero dice qué es lo que está escuchando: con dos
// listeners, un log que dice "panel" para los dos manda a diagnosticar al
// lugar equivocado.
func EscucharComo(nombre, direccion string, h http.Handler, plazo time.Duration) error {
	limite := time.Now().Add(plazo)
	var ultimo error

	for {
		ln, err := net.Listen("tcp", direccion)
		if err == nil {
			slog.Info(nombre+" escuchando", "direccion", direccion)
			srv := &http.Server{
				Handler:           h,
				ReadHeaderTimeout: 10 * time.Second,
			}
			return srv.Serve(ln)
		}
		ultimo = err
		if time.Now().After(limite) {
			return fmt.Errorf("%s: no se pudo escuchar en %s en %s: %w", nombre, direccion, plazo, ultimo)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
