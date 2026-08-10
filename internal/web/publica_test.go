package web_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/web"
)

func estadoDePrueba() web.EstadoPublico {
	return web.EstadoPublico{
		Generado: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Servicios: []web.ServicioPublico{
			{Nombre: "comm-tool", OK: true, UptimePct: 99.9},
			{Nombre: "study-master", OK: false, UptimePct: 97.2},
		},
	}
}

// La invariante 4 del spec. Se arma por lista blanca y no por lista negra: el
// test alimenta el render con TODO lo que no debe salir y verifica que no
// aparezca. Si alguien mañana agrega un campo a la plantilla, acá se entera.
func TestLaPortadaNoFiltraNadaSensible(t *testing.T) {
	var b strings.Builder
	if err := web.RenderPublica(&b, estadoDePrueba()); err != nil {
		t.Fatalf("RenderPublica: %v", err)
	}
	salida := b.String()

	// Ninguna IP tiene por qué salir a internet, ni la pública ni la de
	// tailnet. Se busca el patrón y no los valores reales: este archivo vive
	// en un repo público, y tener las IPs hardcodeadas acá era la fuga.
	ipv4 := regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	if m := ipv4.FindString(salida); m != "" {
		t.Errorf("la portada pública filtra una IP (%q)", m)
	}

	prohibido := map[string]string{
		"supabase-gym-kong":  "un nombre de container",
		"comm-tool-db":       "un nombre de container",
		"connection refused": "el error crudo de un probe",
		"dial tcp":           "el error crudo de un probe",
		"/var/lib":           "una ruta del servidor",
		"/opt/stacks":        "una ruta del servidor",
		"GiB":                "una métrica del host",
	}
	for texto, que := range prohibido {
		if strings.Contains(salida, texto) {
			t.Errorf("la portada pública filtra %s (%q)", que, texto)
		}
	}
}

func TestLaPortadaMuestraLoQueSiCorresponde(t *testing.T) {
	var b strings.Builder
	if err := web.RenderPublica(&b, estadoDePrueba()); err != nil {
		t.Fatal(err)
	}
	salida := b.String()

	for _, quiero := range []string{"comm-tool", "study-master", "99.9", "97.2"} {
		if !strings.Contains(salida, quiero) {
			t.Errorf("la portada no muestra %q", quiero)
		}
	}
}

// El watchdog necesita saber si la página está fresca: Caddy sirve feliz un
// archivo viejo si el proceso dejó de escribirlo, y eso daría 200 igual.
func TestLaPortadaLlevaLaMarcaDeFrescura(t *testing.T) {
	e := estadoDePrueba()
	var b strings.Builder
	if err := web.RenderPublica(&b, e); err != nil {
		t.Fatal(err)
	}

	marca := fmt.Sprintf("<!--generado:%d-->", e.Generado.Unix())
	if !strings.Contains(b.String(), marca) {
		t.Errorf("falta la marca de frescura %q", marca)
	}
}

// Caddy lee el archivo mientras el proceso lo escribe. Sin rename atómico,
// un visitante puede recibir media página.
func TestEscribirEsAtomicoYNoDejaBasura(t *testing.T) {
	dir := t.TempDir()
	destino := filepath.Join(dir, "index.html")

	if err := web.EscribirPublica(destino, estadoDePrueba()); err != nil {
		t.Fatalf("EscribirPublica: %v", err)
	}

	b, err := os.ReadFile(destino)
	if err != nil {
		t.Fatalf("no se creó el archivo: %v", err)
	}
	if !strings.Contains(string(b), "comm-tool") {
		t.Error("el archivo quedó sin contenido")
	}
	// El temporal no puede quedar dando vueltas.
	entradas, _ := os.ReadDir(dir)
	if len(entradas) != 1 {
		t.Errorf("quedaron %d archivos en el directorio, quería 1", len(entradas))
	}
}

func TestEscribirDosVecesPisaSinRomper(t *testing.T) {
	dir := t.TempDir()
	destino := filepath.Join(dir, "index.html")

	for range 3 {
		if err := web.EscribirPublica(destino, estadoDePrueba()); err != nil {
			t.Fatalf("EscribirPublica: %v", err)
		}
	}
	entradas, _ := os.ReadDir(dir)
	if len(entradas) != 1 {
		t.Errorf("quedaron %d archivos, quería 1", len(entradas))
	}
}

// Caddy corre en un container con otro usuario: si el archivo queda 0600,
// sirve un 403 y la portada aparece caída sin ninguna pista.
func TestElArchivoQuedaLegiblePorCaddy(t *testing.T) {
	destino := filepath.Join(t.TempDir(), "index.html")
	if err := web.EscribirPublica(destino, estadoDePrueba()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destino)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o044 == 0 {
		t.Errorf("permisos = %v, Caddy no va a poder leerlo", info.Mode().Perm())
	}
}
