package store

import "testing"

// TestRespuestaRegistrada_MismoBlobMismaPregunta_NoSeRepite cubre el criterio
// de aceptación de T7.5: la misma pregunta sobre el mismo blob no se vuelve a
// preguntar en una segunda ejecución (simulada aquí como una segunda llamada
// a RespuestaRegistrada sobre el mismo Store).
func TestRespuestaRegistrada_MismoBlobMismaPregunta_NoSeRepite(t *testing.T) {
	gitCommonDir := t.TempDir()
	s := NuevoStore(gitCommonDir)

	if err := s.RegistrarRespuesta("blob123", "q1", "sí, es intencional", "iseoane"); err != nil {
		t.Fatalf("RegistrarRespuesta: %v", err)
	}

	// Primera "ejecución": ya se registró la respuesta.
	respuesta, ok, err := s.RespuestaRegistrada("blob123", "q1")
	if err != nil {
		t.Fatalf("RespuestaRegistrada: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, esperado true: la respuesta ya fue registrada")
	}
	if respuesta != "sí, es intencional" {
		t.Errorf("respuesta = %q, esperado %q", respuesta, "sí, es intencional")
	}

	// Segunda "ejecución" (simulada con un Store nuevo sobre el mismo
	// git-common-dir, como ocurriría entre dos invocaciones separadas del
	// proceso): debe seguir encontrando la misma respuesta sin volver a
	// preguntar.
	s2 := NuevoStore(gitCommonDir)
	respuesta2, ok2, err := s2.RespuestaRegistrada("blob123", "q1")
	if err != nil {
		t.Fatalf("RespuestaRegistrada (segunda ejecución): %v", err)
	}
	if !ok2 || respuesta2 != respuesta {
		t.Errorf("segunda ejecución: ok=%v respuesta=%q, esperado ok=true respuesta=%q", ok2, respuesta2, respuesta)
	}
}

// TestRespuestaRegistrada_BlobDistinto_NoEncuentraLaDeOtroBlob confirma que
// la clave incluye el blob: la misma questionID sobre un blob distinto no
// debe encontrarse.
func TestRespuestaRegistrada_BlobDistinto_NoEncuentraLaDeOtroBlob(t *testing.T) {
	s := NuevoStore(t.TempDir())
	if err := s.RegistrarRespuesta("blob123", "q1", "respuesta", "iseoane"); err != nil {
		t.Fatalf("RegistrarRespuesta: %v", err)
	}

	_, ok, err := s.RespuestaRegistrada("blob-otro", "q1")
	if err != nil {
		t.Fatalf("RespuestaRegistrada: %v", err)
	}
	if ok {
		t.Error("ok = true, esperado false: el blob es distinto")
	}
}

// TestRespuestaRegistrada_QuestionIDDistinta_NoEncuentraLaDeOtraPregunta
// confirma que la clave incluye questionID: la misma pregunta sobre el mismo
// blob pero con otra questionID no debe encontrarse.
func TestRespuestaRegistrada_QuestionIDDistinta_NoEncuentraLaDeOtraPregunta(t *testing.T) {
	s := NuevoStore(t.TempDir())
	if err := s.RegistrarRespuesta("blob123", "q1", "respuesta", "iseoane"); err != nil {
		t.Fatalf("RegistrarRespuesta: %v", err)
	}

	_, ok, err := s.RespuestaRegistrada("blob123", "q2")
	if err != nil {
		t.Fatalf("RespuestaRegistrada: %v", err)
	}
	if ok {
		t.Error("ok = true, esperado false: la questionID es distinta")
	}
}

// TestRespuestaRegistrada_SinRegistroPrevio_OkFalseSinError confirma el
// caso de arranque: ninguna respuesta registrada todavía no es un error.
func TestRespuestaRegistrada_SinRegistroPrevio_OkFalseSinError(t *testing.T) {
	s := NuevoStore(t.TempDir())

	respuesta, ok, err := s.RespuestaRegistrada("blob-nuevo", "q-nueva")
	if err != nil {
		t.Fatalf("RespuestaRegistrada: %v", err)
	}
	if ok {
		t.Error("ok = true, esperado false: no hay ninguna respuesta registrada")
	}
	if respuesta != "" {
		t.Errorf("respuesta = %q, esperado vacío", respuesta)
	}
}
