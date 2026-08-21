package git

// ObtenerArchivosStaged returns only paths present in the Git index diff.
// Unlike ObtenerArchivosModificados, it never includes unstaged or untracked
// worktree content.
func ObtenerArchivosStaged() ([]ArchivoModificado, error) {
	output, err := ejecutarGitSalida("diff", "--cached", "--numstat", "--")
	if err != nil {
		return nil, err
	}
	return parsearNumstat(output), nil
}

// MedirVolumenStaged measures the authored additions in the pending commit
// candidate represented by the index. An empty index is a successful zero
// measurement.
func MedirVolumenStaged() (VolumenPendiente, error) {
	files, err := ObtenerArchivosStaged()
	if err != nil {
		return VolumenPendiente{Estado: "ERROR"}, err
	}

	var volume VolumenPendiente
	volume.Paths = make([]string, 0, len(files))
	for _, file := range files {
		volume.Paths = append(volume.Paths, file.Ruta)
		if CuentaParaVolumen(ClaseArchivo(file.Ruta)) {
			volume.Bloqueante += file.Lineas
			continue
		}
		volume.Informativo += file.Lineas
	}
	volume.Estado = clasificarEstado(volume.Bloqueante)
	return volume, nil
}

// CheckStagedDiffLimits returns the blocking volume and state for the index
// diff, preserving the short-form contract used by worktree measurements.
func CheckStagedDiffLimits() (int, string, error) {
	volume, err := MedirVolumenStaged()
	if err != nil {
		return 0, "ERROR", err
	}
	return volume.Bloqueante, volume.Estado, nil
}
