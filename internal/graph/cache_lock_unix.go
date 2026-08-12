//go:build !windows

package graph

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func abrirArchivoBloqueo(raiz *os.Root, nombre string) (*os.File, error) {
	return raiz.OpenFile(nombre, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0600)
}

func intentarBloqueoArchivo(archivo *os.File) (bool, error) {
	err := unix.Flock(int(archivo.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func desbloquearArchivo(archivo *os.File) error {
	return unix.Flock(int(archivo.Fd()), unix.LOCK_UN)
}
