package git

import "testing"

// TestUmbralesDerivanDelLimiteRevisable cubre B4: el 400 vivía en tres sitios
// sin relación entre sí. Ahora hay una sola fuente y los demás derivan de
// ella, así que recalibrar el guardián no puede dejar atrás a la
// fragmentación ni al aislamiento de config.
func TestUmbralesDerivanDelLimiteRevisable(t *testing.T) {
	if limiteLineasLote != LimiteLineasRevisables {
		t.Errorf("limiteLineasLote = %d, debe derivar de LimiteLineasRevisables (%d)",
			limiteLineasLote, LimiteLineasRevisables)
	}
	if limiteConfigGigante != LimiteLineasRevisables {
		t.Errorf("limiteConfigGigante = %d, debe derivar de LimiteLineasRevisables (%d)",
			limiteConfigGigante, LimiteLineasRevisables)
	}
}

// TestValoresDeUmbralNoCambian: T0.4 unifica, no recalibra. Si estos valores
// cambian, es una decisión de producto que debe tomarse aparte.
func TestValoresDeUmbralNoCambian(t *testing.T) {
	casos := []struct {
		nombre string
		actual int
		valor  int
	}{
		{"LimiteLineasRevisables", LimiteLineasRevisables, 400},
		{"UmbralPuntoOptimo", UmbralPuntoOptimo, 200},
		{"LimiteCodigoGigante", LimiteCodigoGigante, 500},
	}
	for _, caso := range casos {
		if caso.actual != caso.valor {
			t.Errorf("%s = %d, esperado %d: esta tarea unifica, no recalibra",
				caso.nombre, caso.actual, caso.valor)
		}
	}
}

// TestClasificarEstadoRespetaLosUmbrales fija las fronteras exactas para que
// derivar las constantes no desplace ninguna sin querer.
func TestClasificarEstadoRespetaLosUmbrales(t *testing.T) {
	casos := []struct {
		lineas   int
		esperado string
	}{
		{0, "PEQUENO"},
		{UmbralPuntoOptimo - 1, "PEQUENO"},
		{UmbralPuntoOptimo, "PUNTO_OPTIMO"},
		{LimiteLineasRevisables, "PUNTO_OPTIMO"},
		{LimiteLineasRevisables + 1, "CRITICO"},
	}
	for _, caso := range casos {
		if estado := clasificarEstado(caso.lineas); estado != caso.esperado {
			t.Errorf("clasificarEstado(%d) = %q, esperado %q", caso.lineas, estado, caso.esperado)
		}
	}
}
