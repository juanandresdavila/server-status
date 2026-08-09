package host_test

import (
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/collector/host"
)

// Formato real de /proc/stat. La primera línea agrega todos los cores;
// las que siguen son por core y hay que ignorarlas.
const statFixture = `cpu  100 20 30 800 40 0 10 0 0 0
cpu0 50 10 15 400 20 0 5 0 0 0
cpu1 50 10 15 400 20 0 5 0 0 0
intr 12345
ctxt 67890
`

func TestParseStatSumaTodoYSeparaElOcio(t *testing.T) {
	got, err := host.ParseStat(strings.NewReader(statFixture))
	if err != nil {
		t.Fatalf("ParseStat: %v", err)
	}

	// Total = 100+20+30+800+40+0+10+0+0+0
	if got.Total != 1000 {
		t.Errorf("Total = %d, quería 1000", got.Total)
	}
	// Idle = idle(800) + iowait(40). Un core esperando disco no está trabajando.
	if got.Idle != 840 {
		t.Errorf("Idle = %d, quería 840", got.Idle)
	}
}

func TestParseStatSinLineaCpuDaError(t *testing.T) {
	_, err := host.ParseStat(strings.NewReader("intr 1\nctxt 2\n"))
	if err == nil {
		t.Fatal("quería error cuando no hay línea 'cpu', no hubo")
	}
}

func TestPercentUsaLosDeltas(t *testing.T) {
	prev := host.CPUTimes{Total: 1000, Idle: 840}
	cur := host.CPUTimes{Total: 2000, Idle: 1640}

	// delta total = 1000, delta idle = 800 → ocupado 200 de 1000 = 20%
	if got := host.Percent(prev, cur); got != 20 {
		t.Errorf("Percent = %v, quería 20", got)
	}
}

func TestPercentSinTiempoTranscurridoDaCero(t *testing.T) {
	same := host.CPUTimes{Total: 1000, Idle: 840}
	if got := host.Percent(same, same); got != 0 {
		t.Errorf("Percent = %v, quería 0", got)
	}
}

const meminfoFixture = `MemTotal:       12000000 kB
MemFree:          260000 kB
MemAvailable:    8400000 kB
Buffers:          100000 kB
SwapTotal:       2097152 kB
SwapFree:        2097100 kB
`

func TestParseMeminfoConvierteABytes(t *testing.T) {
	got, err := host.ParseMeminfo(strings.NewReader(meminfoFixture))
	if err != nil {
		t.Fatalf("ParseMeminfo: %v", err)
	}

	if got.TotalBytes != 12000000*1024 {
		t.Errorf("TotalBytes = %d, quería %d", got.TotalBytes, 12000000*1024)
	}
	// Usado se calcula contra MemAvailable, no contra MemFree: el page cache
	// figura como ocupado pero el kernel lo suelta cuando hace falta.
	if got.UsedBytes() != (12000000-8400000)*1024 {
		t.Errorf("UsedBytes = %d, quería %d", got.UsedBytes(), (12000000-8400000)*1024)
	}
	if got.SwapUsedBytes() != (2097152-2097100)*1024 {
		t.Errorf("SwapUsedBytes = %d, quería %d", got.SwapUsedBytes(), (2097152-2097100)*1024)
	}
}

func TestParseMeminfoAvisaQueCampoFalta(t *testing.T) {
	_, err := host.ParseMeminfo(strings.NewReader("MemTotal: 100 kB\n"))
	if err == nil {
		t.Fatal("quería error por campos faltantes, no hubo")
	}
	if !strings.Contains(err.Error(), "MemAvailable") {
		t.Errorf("el error debería nombrar el campo que falta, dijo: %v", err)
	}
}

func TestParseLoadavg(t *testing.T) {
	got, err := host.ParseLoadavg(strings.NewReader("0.41 0.60 0.53 2/512 12345\n"))
	if err != nil {
		t.Fatalf("ParseLoadavg: %v", err)
	}
	if got.One != 0.41 || got.Five != 0.60 || got.Fifteen != 0.53 {
		t.Errorf("Load = %+v, quería {0.41 0.60 0.53}", got)
	}
}

func TestParseLoadavgTruncadoDaError(t *testing.T) {
	if _, err := host.ParseLoadavg(strings.NewReader("0.41 0.60\n")); err == nil {
		t.Fatal("quería error con menos de 3 campos, no hubo")
	}
}

func TestParseUptime(t *testing.T) {
	got, err := host.ParseUptime(strings.NewReader("13620.50 98765.43\n"))
	if err != nil {
		t.Fatalf("ParseUptime: %v", err)
	}
	quiero := 13620500 * time.Millisecond
	if got != quiero {
		t.Errorf("Uptime = %v, quería %v", got, quiero)
	}
}

// Formato real: dos líneas de encabezado y después "iface: rx... tx...".
// El VPS tiene ens3, tailscale0, docker0 y un montón de veth.
const netDevFixture = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 5000 50 0 0 0 0 0 0 6000 60 0 0 0 0 0 0
  ens3: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0
tailscale0: 300 3 0 0 0 0 0 0 400 4 0 0 0 0 0 0
docker0: 999999 99 0 0 0 0 0 0 999999 99 0 0 0 0 0 0
vethabc123: 777 7 0 0 0 0 0 0 777 7 0 0 0 0 0 0
`

func TestParseNetDevSumaSoloLasInterfacesReales(t *testing.T) {
	got, err := host.ParseNetDev(strings.NewReader(netDevFixture))
	if err != nil {
		t.Fatalf("ParseNetDev: %v", err)
	}

	// lo, docker0 y veth* quedan afuera: son tráfico interno y lo contarían doble.
	// Quedan ens3 (1000/2000) y tailscale0 (300/400).
	if got.RxBytes != 1300 {
		t.Errorf("RxBytes = %d, quería 1300", got.RxBytes)
	}
	if got.TxBytes != 2400 {
		t.Errorf("TxBytes = %d, quería 2400", got.TxBytes)
	}
}
