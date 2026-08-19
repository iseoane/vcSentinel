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

// TestClaveRespuesta_SinColisionConSeparadorEmbebido fija el fix real de esta
// ronda: antes de él, claveRespuesta concatenaba "blob:%s#question:%s" sin
// escapar, así que un blob y un questionID que ya contuvieran el separador
// podían colisionar entre pares distintos. Si este test se revierte junto
// con claveRespuesta a esa concatenación simple, debe fallar: ambos pares
// producían literalmente el mismo string "blob:X#question:q1#question:q2".
func TestClaveRespuesta_SinColisionConSeparadorEmbebido(t *testing.T) {
	a := claveRespuesta("X#question:q1", "q2")
	b := claveRespuesta("X", "q1#question:q2")
	if a == b {
		t.Fatalf("claveRespuesta colisiona: (%q,%q) y (%q,%q) producen la misma clave %q",
			"X#question:q1", "q2", "X", "q1#question:q2", a)
	}
}

// TestRespuestaRegistrada_ComponentesConSeparadorEmbebido_NoColisionan es la
// misma colisión pero a través de la API pública: registrar una respuesta
// para un par (blob, questionID) no debe hacerse visible para un par
// DISTINTO cuya concatenación naive coincidiría con la del primero.
func TestRespuestaRegistrada_ComponentesConSeparadorEmbebido_NoColisionan(t *testing.T) {
	s := NuevoStore(t.TempDir())
	if err := s.RegistrarRespuesta("X#question:q1", "q2", "respuesta del primer par", "iseoane"); err != nil {
		t.Fatalf("RegistrarRespuesta: %v", err)
	}

	_, ok, err := s.RespuestaRegistrada("X", "q1#question:q2")
	if err != nil {
		t.Fatalf("RespuestaRegistrada: %v", err)
	}
	if ok {
		t.Error("ok = true, esperado false: es un par (blob, questionID) distinto, no debe encontrar la respuesta del otro par")
	}
}

// TestRespuestaRegistrada_DosRespuestas_DevuelveLaMasReciente confirma el
// comentario de RespuestaRegistrada: si la misma (blob, questionID) se
// respondió más de una vez (decisions.jsonl es append-only y no lo impide),
// se devuelve la respuesta más reciente, no la primera.
func TestRespuestaRegistrada_DosRespuestas_DevuelveLaMasReciente(t *testing.T) {
	s := NuevoStore(t.TempDir())
	if err := s.RegistrarRespuesta("blob123", "q1", "primera respuesta", "iseoane"); err != nil {
		t.Fatalf("RegistrarRespuesta (primera): %v", err)
	}
	if err := s.RegistrarRespuesta("blob123", "q1", "segunda respuesta", "iseoane"); err != nil {
		t.Fatalf("RegistrarRespuesta (segunda): %v", err)
	}

	respuesta, ok, err := s.RespuestaRegistrada("blob123", "q1")
	if err != nil {
		t.Fatalf("RespuestaRegistrada: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, esperado true")
	}
	if respuesta != "segunda respuesta" {
		t.Errorf("respuesta = %q, esperado la más reciente %q", respuesta, "segunda respuesta")
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
