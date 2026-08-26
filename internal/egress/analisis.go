package egress

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Umbrales de la regla pre-registrada. Están acá y no sueltos en el análisis
// para que no haya dos versiones del criterio.
const (
	EventosMinimos = 10   // eventos en el brazo alto para poder concluir
	PMaximo        = 0.01 // Fisher exacto de dos colas

	// Por encima de esta tasa un brazo no está midiendo blips intermitentes:
	// esa familia no tiene salida, o el destino no existe. El fenómeno que se
	// investiga anda por el 0,4 %, así que no hay riesgo de disparar de más.
	TasaDeBrazoCaido = 0.5
	IntentosMinimos  = 10
)

type ResumenBrazo struct {
	Brazo    string
	Intentos int
	Fallas   int
	Resets   int
	Timeouts int
	Otras    int
	// Inconsistentes son los intentos cuya familia REAL no coincide con la del
	// brazo. Cualquier valor distinto de cero invalida el experimento.
	Inconsistentes int
}

func (r ResumenBrazo) Buenos() int { return r.Intentos - r.Fallas }

func (r ResumenBrazo) Tasa() float64 {
	if r.Intentos == 0 {
		return 0
	}
	return float64(r.Fallas) / float64(r.Intentos)
}

func sumar(a, b ResumenBrazo) ResumenBrazo {
	return ResumenBrazo{
		Brazo:          a.Brazo + "+" + b.Brazo,
		Intentos:       a.Intentos + b.Intentos,
		Fallas:         a.Fallas + b.Fallas,
		Resets:         a.Resets + b.Resets,
		Timeouts:       a.Timeouts + b.Timeouts,
		Otras:          a.Otras + b.Otras,
		Inconsistentes: a.Inconsistentes + b.Inconsistentes,
	}
}

// LeerJSONL lee el archivo de la medición. Una línea rota no aborta la lectura:
// el proceso puede haber muerto a mitad de un write y perder la última línea no
// puede costar el experimento entero.
func LeerJSONL(r io.Reader) ([]Registro, int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var rs []Registro
	rotas := 0
	for sc.Scan() {
		linea := strings.TrimSpace(sc.Text())
		if linea == "" {
			continue
		}
		var reg Registro
		if err := json.Unmarshal([]byte(linea), &reg); err != nil {
			rotas++
			continue
		}
		rs = append(rs, reg)
	}
	return rs, rotas, sc.Err()
}

// BucketTick es la cadencia más fina del experimento. Redondear a ella agrupa
// los intentos de un mismo tick aunque uno caiga del otro lado del segundo.
const BucketTick = 30 * time.Second

// TickDe devuelve el instante del tick al que pertenece un intento.
func TickDe(r Registro) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, r.TS)
	if err != nil {
		return time.Time{}, false
	}
	return t.Round(BucketTick), true
}

// AlineadoAlMinuto dice si el tick cae en el offset :00, que es el único que
// miran TODOS los brazos. Los ticks :30 los ve solo v6-ka-30s.
func AlineadoAlMinuto(t time.Time) bool { return t.Second() == 0 }

// ResumirPorTick cuenta TICKS, no intentos: un tick falla si falló alguno de
// sus destinos.
//
// Es la corrección del 26/08/2026 a la regla pre-registrada. Un blip corta los
// cinco destinos en el mismo segundo, así que contarlos como cinco
// observaciones independientes le da a Fisher cinco veces la evidencia que hay
// — y Fisher supone independencia. La unidad tiene que ser el tick.
//
// Con soloAlMinuto, se queda con los ticks :00. Ahí v6-ka y v6-ka-30s tienen
// exposición IDÉNTICA —mismos instantes, mismos destinos— y difieren solo en el
// ocio (60 s contra 30 s), que es lo único que el desempate quiere medir.
func ResumirPorTick(rs []Registro, soloAlMinuto bool) map[string]ResumenBrazo {
	type clave struct {
		brazo string
		tick  time.Time
	}
	falla := map[clave]bool{}
	clase := map[clave]Clase{}
	inconsistentes := map[string]int{}
	red := map[string]string{}

	for _, r := range rs {
		t, ok := TickDe(r)
		if !ok || (soloAlMinuto && !AlineadoAlMinuto(t)) {
			continue
		}
		k := clave{r.Brazo, t}
		if _, visto := falla[k]; !visto {
			falla[k] = false
		}
		red[r.Brazo] = r.Red
		if r.Clase.EsFalla() {
			falla[k] = true
			// La clase del tick es la del primer destino que falló: alcanza
			// para separar resets de timeouts, que es para lo que se usa.
			if _, hay := clase[k]; !hay {
				clase[k] = r.Clase
			}
		}
		if fr := r.FamiliaReal(); fr != "" && fr != familiaDeRed(r.Red) {
			inconsistentes[r.Brazo]++
		}
	}

	m := map[string]ResumenBrazo{}
	for k, hubo := range falla {
		s := m[k.brazo]
		s.Brazo = k.brazo
		s.Intentos++
		if hubo {
			s.Fallas++
			switch clase[k] {
			case ClaseReset:
				s.Resets++
			case ClaseTimeout:
				s.Timeouts++
			default:
				s.Otras++
			}
		}
		m[k.brazo] = s
	}
	for b, n := range inconsistentes {
		s := m[b]
		s.Inconsistentes = n
		m[b] = s
	}
	return m
}

func Resumir(rs []Registro) map[string]ResumenBrazo {
	m := map[string]ResumenBrazo{}
	for _, r := range rs {
		s := m[r.Brazo]
		s.Brazo = r.Brazo
		s.Intentos++
		switch r.Clase {
		case ClaseOK:
		case ClaseReset:
			s.Fallas++
			s.Resets++
		case ClaseTimeout:
			s.Fallas++
			s.Timeouts++
		default:
			s.Fallas++
			s.Otras++
		}
		// Solo se puede juzgar la familia de los intentos que llegaron a tener
		// conexión: los que fallaron al dialar no tienen IP remota.
		if fr := r.FamiliaReal(); fr != "" && fr != familiaDeRed(r.Red) {
			s.Inconsistentes++
		}
		m[r.Brazo] = s
	}
	return m
}

func familiaDeRed(red string) string {
	switch red {
	case "tcp4":
		return "v4"
	case "tcp6":
		return "v6"
	}
	return ""
}

// Separados aplica el umbral pre-registrado: ≥10 eventos en el brazo alto y
// Fisher exacto de dos colas p < 0,01. Devuelve también el p para poder
// mostrarlo, porque un umbral sin el número atrás no se puede discutir.
func Separados(a, b ResumenBrazo) (bool, float64) {
	alto, bajo := a, b
	if bajo.Fallas > alto.Fallas {
		alto, bajo = bajo, alto
	}
	p := FisherExacto(alto.Fallas, alto.Buenos(), bajo.Fallas, bajo.Buenos())
	return alto.Fallas >= EventosMinimos && p < PMaximo, p
}

type Veredicto struct {
	Codigo  string
	Detalle string
}

// Códigos posibles. Son exactamente las filas de la tabla pre-registrada.
const (
	VerdInstrumento = "instrumento-infiel"
	VerdBrazoCaido  = "brazo-caido"
	VerdSinFallas   = "sin-fallas"
	VerdFamilia     = "h1-familia"
	VerdReuso       = "h2-reuso"
	VerdAmbas       = "h1-y-h2"
	VerdUplink      = "uplink"
	VerdNoConcluye  = "no-concluye"
)

// Concluir aplica la regla de decisión de
// docs/superpowers/plans/2026-08-26-egress-ipv6-vs-ipv4.md, escrita antes de
// que existieran los datos. La conclusión no se saca a ojo.
func Concluir(m map[string]ResumenBrazo) Veredicto {
	v6ka, ok1 := m["v6-ka"]
	v4ka, ok2 := m["v4-ka"]
	v6f, ok3 := m["v6-fresh"]
	v4f, ok4 := m["v4-fresh"]
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return Veredicto{VerdNoConcluye, "faltan brazos del factorial: no hay 2×2 que leer"}
	}

	for _, b := range []ResumenBrazo{v6ka, v4ka, v6f, v4f} {
		if b.Inconsistentes > 0 {
			return Veredicto{VerdInstrumento, fmt.Sprintf(
				"el brazo %s registró %d intentos por la familia equivocada: no está midiendo lo que dice su nombre",
				b.Brazo, b.Inconsistentes)}
		}
	}

	// Antes del 2×2: un brazo que falla siempre no aporta un contrafactual, y
	// leerlo como "esta familia falla" confundiría "no hay salida por acá" con
	// "esta familia tiene blips".
	for _, b := range []ResumenBrazo{v6ka, v4ka, v6f, v4f} {
		if b.Intentos >= IntentosMinimos && b.Tasa() >= TasaDeBrazoCaido {
			return Veredicto{VerdBrazoCaido, fmt.Sprintf(
				"el brazo %s falla en el %.0f%% de los intentos (%d/%d): eso no es un blip intermitente, "+
					"es que esa familia no tiene salida o el destino no existe. Arreglar eso antes de leer el 2×2",
				b.Brazo, b.Tasa()*100, b.Fallas, b.Intentos)}
		}
	}

	v6, v4 := sumar(v6ka, v6f), sumar(v4ka, v4f)
	ka, fresh := sumar(v6ka, v4ka), sumar(v6f, v4f)

	if v6.Fallas+v4.Fallas == 0 {
		return Veredicto{VerdSinFallas, "cero fallas en los cuatro brazos: decide el control positivo contra probe_results"}
	}

	porFamilia, pFam := Separados(v6, v4)
	porReuso, pReu := Separados(ka, fresh)

	switch {
	case porFamilia && !porReuso:
		return Veredicto{VerdFamilia, fmt.Sprintf(
			"v6 %d/%d contra v4 %d/%d, p=%.2g. Es la familia: forzar tcp4 en el DialContext del prober",
			v6.Fallas, v6.Intentos, v4.Fallas, v4.Intentos, pFam)}
	case porReuso && !porFamilia:
		return Veredicto{VerdReuso, fmt.Sprintf(
			"keep-alive %d/%d contra fresh %d/%d, p=%.2g. NO es la familia: es el reuso de conexiones ociosas. Forzar tcp4 habría 'funcionado' por accidente",
			ka.Fallas, ka.Intentos, fresh.Fallas, fresh.Intentos, pReu)}
	case porFamilia && porReuso:
		return Veredicto{VerdAmbas, fmt.Sprintf(
			"separan los dos ejes (familia p=%.2g, reuso p=%.2g): se pierde estado de flujo y además solo por v6",
			pFam, pReu)}
	}

	if v6ka.Fallas >= EventosMinimos && v4ka.Fallas >= EventosMinimos &&
		v6f.Fallas >= EventosMinimos && v4f.Fallas >= EventosMinimos {
		return Veredicto{VerdUplink, fmt.Sprintf(
			"fallan los cuatro brazos y ningún eje separa (familia p=%.2g, reuso p=%.2g): es el uplink del VPS, no hay cambio de código que lo arregle",
			pFam, pReu)}
	}
	return Veredicto{VerdNoConcluye, fmt.Sprintf(
		"ningún eje separa (familia p=%.2g, reuso p=%.2g) y no hay fallas parejas en los cuatro brazos: comparar los INSTANTES de las fallas de v4 y v6",
		pFam, pReu)}
}

// Desempate30s mira el brazo de cadencia, que no entra en la tabla principal.
func Desempate30s(m map[string]ResumenBrazo) string {
	v6ka, ok1 := m["v6-ka"]
	corto, ok2 := m["v6-ka-30s"]
	if !ok1 || !ok2 {
		return "sin brazo de 30 s"
	}
	if v6ka.Fallas == 0 {
		return "v6-ka no falló: nada que desempatar"
	}
	sep, p := Separados(v6ka, corto)
	if sep && v6ka.Fallas > corto.Fallas {
		return fmt.Sprintf("v6-ka %d/%d contra v6-ka-30s %d/%d, p=%.2g: con 30 s de ocio no falla, "+
			"así que el estado del flujo expira entre 30 y 60 s",
			v6ka.Fallas, v6ka.Intentos, corto.Fallas, corto.Intentos, p)
	}
	return fmt.Sprintf("v6-ka %d/%d contra v6-ka-30s %d/%d, p=%.2g: no separan, "+
		"el tiempo de ocio no explica la diferencia",
		v6ka.Fallas, v6ka.Intentos, corto.Fallas, corto.Intentos, p)
}

// Informe arma la salida legible del modo -analizar. Muestra las DOS lecturas
// —por intento, como se pre-registró, y por tick, que es la corregida— y saca
// el veredicto de la segunda.
func Informe(rs []Registro, rotas int) string {
	var b strings.Builder

	fmt.Fprint(&b, "POR INTENTO (como se pre-registró; las fallas de un mismo blip no son independientes)\n")
	fmt.Fprint(&b, tabla(Resumir(rs)))

	porTick := ResumirPorTick(rs, false)
	fmt.Fprint(&b, "\nPOR TICK (corregida: un tick falla si falló alguno de sus destinos)\n")
	fmt.Fprint(&b, tabla(porTick))

	if rotas > 0 {
		fmt.Fprintf(&b, "\n(%d líneas ilegibles, ignoradas)\n", rotas)
	}

	v := Concluir(porTick)
	fmt.Fprintf(&b, "\nveredicto (sobre los ticks): %s\n  %s\n", v.Codigo, v.Detalle)

	fmt.Fprintf(&b, "\ndesempate de 30 s, solo en los ticks :00 —donde los dos brazos miran\n"+
		"los mismos instantes y difieren solo en el ocio—:\n  %s\n",
		Desempate30s(ResumirPorTick(rs, true)))

	fmt.Fprint(&b, "\nla regla es la de docs/superpowers/plans/2026-08-26-egress-ipv6-vs-ipv4.md,\n"+
		"escrita antes de que existieran estos datos, con la enmienda del 26/08 anotada ahí.\n")
	return b.String()
}

func tabla(m map[string]ResumenBrazo) string {
	var b strings.Builder
	nombres := make([]string, 0, len(m))
	for n := range m {
		nombres = append(nombres, n)
	}
	sort.Strings(nombres)

	fmt.Fprintf(&b, "  %-12s %9s %8s %9s %8s %9s %7s\n",
		"brazo", "n", "fallas", "tasa", "resets", "timeouts", "otras")
	for _, n := range nombres {
		s := m[n]
		fmt.Fprintf(&b, "  %-12s %9d %8d %8.3f%% %8d %9d %7d\n",
			s.Brazo, s.Intentos, s.Fallas, s.Tasa()*100, s.Resets, s.Timeouts, s.Otras)
	}
	return b.String()
}
