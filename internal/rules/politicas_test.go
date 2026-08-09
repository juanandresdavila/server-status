package rules_test

import (
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/rules"
)

func TestDosFallasNoAbrenIncidente(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	for i := range 2 {
		var tr rules.Transicion
		c, tr = p.Aplicar(c, false, false)
		if tr != rules.SinCambio {
			t.Fatalf("falla %d disparó %v, el VPS está a 179 ms y un hipo no es una caída", i+1, tr)
		}
	}
}

func TestLaTerceraFallaAbre(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	c, _ = p.Aplicar(c, false, false)
	c, _ = p.Aplicar(c, false, false)
	_, tr := p.Aplicar(c, false, false)

	if tr != rules.Abre {
		t.Errorf("transición = %v, quería Abre", tr)
	}
}

// Un éxito en el medio resetea: tres fallas SEGUIDAS, no tres fallas.
func TestUnExitoEnElMedioResetea(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	c, _ = p.Aplicar(c, false, false)
	c, _ = p.Aplicar(c, false, false)
	c, _ = p.Aplicar(c, true, false) // se recuperó
	c, _ = p.Aplicar(c, false, false)
	_, tr := p.Aplicar(c, false, false)

	if tr == rules.Abre {
		t.Error("abrió con dos fallas seguidas: el éxito no reseteó el contador")
	}
}

func TestUnSoloExitoNoCierra(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	_, tr := p.Aplicar(c, true, true) // abierto = true
	if tr != rules.SinCambio {
		t.Error("un solo éxito cerró el incidente: un servicio que rebota no se recuperó")
	}
}

func TestElSegundoExitoCierra(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	c, _ = p.Aplicar(c, true, true)
	_, tr := p.Aplicar(c, true, true)

	if tr != rules.Cierra {
		t.Errorf("transición = %v, quería Cierra", tr)
	}
}

// Con el incidente ya abierto, seguir fallando no vuelve a abrir:
// es "silencio en el medio", la política que eligió el usuario.
func TestConIncidenteAbiertoLasFallasNoDicenNada(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	for range 10 {
		var tr rules.Transicion
		c, tr = p.Aplicar(c, false, true)
		if tr != rules.SinCambio {
			t.Fatalf("una falla con el incidente abierto disparó %v", tr)
		}
	}
}

func umbralDisco() rules.PorUmbral {
	return rules.PorUmbral{Abre: 80, Cierra: 75, Sostenido: 5 * time.Minute}
}

func TestCruzarElUmbralNoAlcanzaSiNoSeSostiene(t *testing.T) {
	p := umbralDisco()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var e rules.EstadoUmbral

	e, tr := p.Aplicar(e, 82, t0, false)
	if tr != rules.SinCambio {
		t.Fatal("abrió apenas cruzó el umbral, sin esperar los 5 min")
	}
	_, tr = p.Aplicar(e, 82, t0.Add(4*time.Minute), false)
	if tr != rules.SinCambio {
		t.Error("abrió a los 4 min, el mínimo son 5")
	}
}

func TestSostenidoElTiempoSuficienteAbre(t *testing.T) {
	p := umbralDisco()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var e rules.EstadoUmbral

	e, _ = p.Aplicar(e, 82, t0, false)
	_, tr := p.Aplicar(e, 82, t0.Add(5*time.Minute), false)

	if tr != rules.Abre {
		t.Errorf("transición = %v, quería Abre", tr)
	}
}

// Bajar del umbral resetea el cronómetro: si no, un pico de disco a las 9 y
// otro a las 18 se sumarían y abrirían un incidente que nunca existió.
func TestBajarDelUmbralReseteaElCronometro(t *testing.T) {
	p := umbralDisco()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var e rules.EstadoUmbral

	e, _ = p.Aplicar(e, 82, t0, false)
	e, _ = p.Aplicar(e, 60, t0.Add(time.Minute), false) // bajó
	e, _ = p.Aplicar(e, 82, t0.Add(2*time.Minute), false)
	_, tr := p.Aplicar(e, 82, t0.Add(6*time.Minute), false)

	if tr == rules.Abre {
		t.Error("abrió a los 4 min del segundo cruce: el cronómetro no se reseteó")
	}
}

// Esta es la razón de ser de la histéresis: un disco parado en 79,8% no puede
// mandar cuarenta mensajes por noche.
func TestQuedarseJustoDebajoDelUmbralNoCierra(t *testing.T) {
	p := umbralDisco()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var e rules.EstadoUmbral

	_, tr := p.Aplicar(e, 79.8, t0, true) // incidente abierto
	if tr == rules.Cierra {
		t.Error("cerró con 79,8%: el cierre es al 75%, justamente para no flapear")
	}
}

func TestBajarDelUmbralDeCierreCierra(t *testing.T) {
	p := umbralDisco()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var e rules.EstadoUmbral

	_, tr := p.Aplicar(e, 74, t0, true)
	if tr != rules.Cierra {
		t.Errorf("transición = %v, quería Cierra con 74%% contra un cierre de 75%%", tr)
	}
}
