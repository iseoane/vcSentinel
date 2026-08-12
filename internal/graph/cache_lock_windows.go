//go:build windows

package graph

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func abrirArchivoBloqueo(raiz *os.Root, nombre string) (*os.File, error) {
	return raiz.OpenFile(nombre, os.O_RDWR|os.O_CREATE, 0600)
}

func intentarBloqueoArchivo(archivo *os.File) (bool, error) {
	var solapado windows.Overlapped
	err := windows.LockFileEx(windows.Handle(archivo.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &solapado)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

func desbloquearArchivo(archivo *os.File) error {
	var solapado windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(archivo.Fd()), 0, 1, 0, &solapado)
}
