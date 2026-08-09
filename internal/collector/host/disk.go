//go:build linux || darwin

package host

import "syscall"

// Disk es el uso del filesystem donde vive una ruta.
type Disk struct {
	TotalBytes uint64
	UsedBytes  uint64
}

// DiskUsage pregunta al kernel por statfs.
//
// El build tag de arriba es lo que permite correr los tests en la Mac: los
// campos de Statfs_t tienen tipos distintos en Linux y en Darwin, y las
// conversiones explícitas a uint64 compilan en los dos.
//
// Usado se calcula como Blocks-Bfree, igual que df. Eso incluye los bloques
// reservados para root, así que el porcentaje puede diferir un punto o dos
// del "Use%" de df, que los descuenta.
func DiskUsage(ruta string) (Disk, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(ruta, &st); err != nil {
		return Disk{}, err
	}
	tam := uint64(st.Bsize)
	total := uint64(st.Blocks) * tam
	libre := uint64(st.Bfree) * tam
	return Disk{TotalBytes: total, UsedBytes: total - libre}, nil
}
