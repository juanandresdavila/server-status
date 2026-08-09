package web

import (
	"html/template"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// ServicioPublico es lo ÚNICO que sale a internet de cada servicio.
//
// No es model.ProbeResult a propósito: la lista blanca se hace con el tipo y
// no con la disciplina de quien escribe la plantilla. La plantilla no puede
// filtrar lo que nunca recibió — ni el código de estado, ni la latencia, ni
// el error crudo, que diría por ejemplo "dial tcp 127.0.0.1:8787" y publicaría
// el mapa interno.
type ServicioPublico struct {
	Nombre    string
	OK        bool
	UptimePct float64
}

// EstadoPublico es todo lo que la portada conoce.
type EstadoPublico struct {
	Generado  time.Time
	Servicios []ServicioPublico
}

// TodoBien dice si mostrar el cartel verde grande.
func (e EstadoPublico) TodoBien() bool {
	for _, s := range e.Servicios {
		if !s.OK {
			return false
		}
	}
	return true
}

// MarcaDeFrescura la lee el watchdog. Un 200 no alcanza para saber que el
// sistema está vivo: Caddy sirve feliz un archivo viejo si el proceso dejó de
// escribirlo.
func (e EstadoPublico) MarcaDeFrescura() template.HTML {
	return template.HTML("<!--generado:" + strconv.FormatInt(e.Generado.Unix(), 10) + "-->")
}

var plantillaPublica = template.Must(
	template.ParseFS(plantillas, "plantillas/publica.html"))

func RenderPublica(w io.Writer, e EstadoPublico) error {
	return plantillaPublica.ExecuteTemplate(w, "publica.html", e)
}

// EscribirPublica renderiza a un temporal y renombra. El rename es atómico
// dentro del mismo filesystem: Caddy nunca ve media página.
func EscribirPublica(destino string, e EstadoPublico) error {
	dir := filepath.Dir(destino)
	tmp, err := os.CreateTemp(dir, ".index-*.html")
	if err != nil {
		return err
	}
	// No-op si el rename salió bien; limpia el temporal si algo falló antes.
	defer os.Remove(tmp.Name())

	if err := RenderPublica(tmp, e); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp deja 0600 y Caddy corre en un container con otro usuario:
	// sin esto sirve un 403 y la portada aparece caída sin ninguna pista.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), destino)
}
