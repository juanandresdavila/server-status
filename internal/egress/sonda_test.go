package egress_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/egress"
)

// servidorVacio contesta 200 sin cuerpo. Sin cuerpo, Go puede devolver la
// conexión al pool sin drenarla — que es lo que pasa en producción contra
// Cloudflare, donde el reuso lo garantiza HTTP/2.
func servidorVacio(t *testing.T, red string) *httptest.Server {
	t.Helper()
	l, err := net.Listen(red, localDe(red))
	if err != nil {
		t.Skipf("no hay %s en esta máquina: %v", red, err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func localDe(red string) string {
	if red == "tcp6" {
		return "[::1]:0"
	}
	return "127.0.0.1:0"
}

func sonda(t *testing.T, nombre, red string, reusa bool) *egress.Sonda {
	t.Helper()
	return egress.NuevaSonda(
		clock.NewFake(time.Date(2026, 8, 26, 20, 0, 0, 0, time.UTC)),
		egress.Brazo{Nombre: nombre, Red: red, Reusa: reusa, Cadencia: time.Minute},
		5*time.Second,
	)
}

func TestMedirRegistraUnIntentoBueno(t *testing.T) {
	srv := servidorVacio(t, "tcp4")
	s := sonda(t, "v4-ka", "tcp4", true)

	got := s.Medir(context.Background(), egress.Destino{Nombre: "x", URL: srv.URL})

	if got.Clase != egress.ClaseOK {
		t.Fatalf("Clase = %q (%s)", got.Clase, got.Error)
	}
	if got.Status != 200 {
		t.Errorf("Status = %d", got.Status)
	}
	if got.TotalMs <= 0 {
		t.Errorf("TotalMs = %v, tiene que medir algo", got.TotalMs)
	}
	if got.Brazo != "v4-ka" || got.Red != "tcp4" || got.Destino != "x" {
		t.Errorf("no etiquetó bien el intento: %+v", got)
	}
	if got.TS != "2026-08-26T20:00:00Z" {
		t.Errorf("TS = %q: el instante sale del reloj inyectado, no de time.Now()", got.TS)
	}
}

// ESTE es el test que sostiene el experimento entero. Si los dos brazos reusan
// —o si ninguno reusa— el eje "reuso" del factorial no mueve nada y el 2×2 no
// distingue H1 de H2: mediría la familia dos veces.
func TestElBrazoKaReusaLaConexionYElFreshNo(t *testing.T) {
	srv := servidorVacio(t, "tcp4")
	d := egress.Destino{Nombre: "x", URL: srv.URL}

	t.Run("ka reusa", func(t *testing.T) {
		s := sonda(t, "v4-ka", "tcp4", true)
		if r := s.Medir(context.Background(), d); r.Reusado {
			t.Fatalf("el primer intento no puede reusar nada: %+v", r)
		}
		r := s.Medir(context.Background(), d)
		if r.Clase != egress.ClaseOK {
			t.Fatalf("Clase = %q (%s)", r.Clase, r.Error)
		}
		if !r.Reusado {
			t.Error("Reusado = false: el brazo ka no está reusando la conexión")
		}
	})

	t.Run("fresh no reusa", func(t *testing.T) {
		s := sonda(t, "v4-fresh", "tcp4", false)
		s.Medir(context.Background(), d)
		r := s.Medir(context.Background(), d)
		if r.Clase != egress.ClaseOK {
			t.Fatalf("Clase = %q (%s)", r.Clase, r.Error)
		}
		if r.Reusado {
			t.Error("Reusado = true: el brazo fresh está reusando la conexión")
		}
		if r.ConexionMs <= 0 {
			t.Error("ConexionMs = 0: si abrió conexión nueva, tuvo que costar un connect")
		}
	})
}

// El otro eje. Un brazo que dice tcp6 y cae a v4 por el fallback de Go
// convertiría el experimento en dos réplicas de lo mismo, y "v4 limpio" sería
// otra vez una afirmación sin contrafactual.
func TestLaFamiliaSeFuerzaDeVerdad(t *testing.T) {
	t.Run("tcp6 no alcanza un servidor solo-v4", func(t *testing.T) {
		srv := servidorVacio(t, "tcp4")
		s := sonda(t, "v6-ka", "tcp6", true)

		r := s.Medir(context.Background(), egress.Destino{Nombre: "x", URL: srv.URL})
		if r.Clase == egress.ClaseOK {
			t.Fatalf("el brazo tcp6 llegó a un servidor que solo escucha v4: cayó a v4 (%+v)", r)
		}
	})

	t.Run("tcp6 sí alcanza un servidor v6", func(t *testing.T) {
		srv := servidorVacio(t, "tcp6")
		s := sonda(t, "v6-ka", "tcp6", true)

		r := s.Medir(context.Background(), egress.Destino{Nombre: "x", URL: srv.URL})
		if r.Clase != egress.ClaseOK {
			t.Fatalf("Clase = %q (%s): el brazo tcp6 no anda ni contra v6", r.Clase, r.Error)
		}
		if got := r.FamiliaReal(); got != "v6" {
			t.Errorf("FamiliaReal() = %q, quería v6 (remota %q)", got, r.Remota)
		}
	})
}

func TestFamiliaRealLeeLaIPRemota(t *testing.T) {
	casos := []struct{ remota, quiero string }{
		{"[2606:4700:130::1]:443", "v6"},
		{"198.51.100.7:443", "v4"},
		{"", ""},
		{"nada", ""},
	}
	for _, c := range casos {
		if got := (egress.Registro{Remota: c.remota}).FamiliaReal(); got != c.quiero {
			t.Errorf("FamiliaReal(%q) = %q, quería %q", c.remota, got, c.quiero)
		}
	}
}

// El JSONL vive en el VPS, pero de ahí se copian tablas y se pegan en
// documentación. La IP de origen no puede llegar ni al archivo.
func TestElErrorDelRegistroVaRedactado(t *testing.T) {
	srv := servidorVacio(t, "tcp4")
	dir := srv.Listener.Addr().String()
	srv.Close() // nadie escuchando: el dial falla y el error trae la dirección

	s := sonda(t, "v4-ka", "tcp4", true)
	r := s.Medir(context.Background(), egress.Destino{Nombre: "x", URL: "http://" + dir + "/"})

	if r.Clase == egress.ClaseOK {
		t.Fatalf("esperaba una falla, dio %+v", r)
	}
	if strings.Contains(r.Error, "->") && !strings.Contains(r.Error, "<origen>") {
		t.Errorf("el error trae un par origen→destino sin redactar: %q", r.Error)
	}
}

func TestEsperaHastaAlinear(t *testing.T) {
	casos := []struct {
		nombre   string
		ahora    time.Time
		cadencia time.Duration
		quiero   time.Duration
	}{
		{"a mitad del minuto", time.Date(2026, 8, 26, 20, 0, 10, 0, time.UTC), time.Minute, 50 * time.Second},
		{"con nanosegundos", time.Date(2026, 8, 26, 20, 0, 59, 500e6, time.UTC), time.Minute, 500 * time.Millisecond},
		{"justo en el borde espera el período entero", time.Date(2026, 8, 26, 20, 0, 0, 0, time.UTC), time.Minute, time.Minute},
		{"cadencia de 30 s", time.Date(2026, 8, 26, 20, 0, 10, 0, time.UTC), 30 * time.Second, 20 * time.Second},
		{"cadencia inválida", time.Date(2026, 8, 26, 20, 0, 10, 0, time.UTC), 0, 0},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := egress.EsperaHastaAlinear(c.ahora, c.cadencia); got != c.quiero {
				t.Errorf("EsperaHastaAlinear = %v, quería %v", got, c.quiero)
			}
		})
	}
}

// Los brazos por defecto tienen que ser un factorial de verdad: las cuatro
// combinaciones de familia × reuso, más el de cadencia.
func TestBrazosPorDefectoCubrenElFactorial(t *testing.T) {
	visto := map[string]bool{}
	for _, b := range egress.BrazosPorDefecto() {
		if b.Red != "tcp4" && b.Red != "tcp6" {
			t.Errorf("brazo %q con red %q", b.Nombre, b.Red)
		}
		if b.Cadencia <= 0 {
			t.Errorf("brazo %q sin cadencia", b.Nombre)
		}
		if b.Cadencia == time.Minute {
			visto[b.Red+"/"+map[bool]string{true: "ka", false: "fresh"}[b.Reusa]] = true
		}
	}
	for _, quiero := range []string{"tcp4/ka", "tcp6/ka", "tcp4/fresh", "tcp6/fresh"} {
		if !visto[quiero] {
			t.Errorf("falta la celda %q del factorial", quiero)
		}
	}
}

// Invariante 6: los probes salen por la URL pública, nunca por localhost.
func TestNingunDestinoApuntaALocalhost(t *testing.T) {
	for _, d := range egress.DestinosPorDefecto() {
		if !strings.HasPrefix(d.URL, "https://") {
			t.Errorf("destino %q no va por https: %q", d.Nombre, d.URL)
		}
		for _, malo := range []string{"localhost", "127.0.0.1", "[::1]", "192.168.", "10."} {
			if strings.Contains(d.URL, malo) {
				t.Errorf("destino %q apunta adentro (%q): invariante 6", d.Nombre, d.URL)
			}
		}
	}
}
