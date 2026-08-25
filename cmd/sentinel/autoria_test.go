package main

import (
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

func adaptadorDe(binario, modelo, esfuerzo string) *agentadapter.CLIAdapter {
	return &agentadapter.CLIAdapter{
		BinaryName: binario,
		Config:     config.AgentConfig{Model: modelo, ReasoningEffort: esfuerzo},
	}
}

// agenteQueFalla implementa review.AuditorAgente devolviendo siempre error,
// para comprobar que una petición fallida no atribuye autoría a nadie.
type agenteQueFalla struct{ *agentadapter.CLIAdapter }

func (agenteQueFalla) EjecutarPrompt(string) (string, error) {
	return "", errors.New("no disponible")
}

type agenteSoloPrompt struct {
	prompts int
}

type policyRecordingAgent struct{ policy reviewcontract.ToolPolicy }

func (*policyRecordingAgent) EjecutarPrompt(string) (string, error) { return "ok", nil }

func (a *policyRecordingAgent) ReviewWithPolicy(_ string, _ string, _ []string, policy reviewcontract.ToolPolicy) (string, error) {
	a.policy = policy
	return "ok", nil
}

func (a *agenteSoloPrompt) EjecutarPrompt(string) (string, error) {
	a.prompts++
	return "no debe ejecutarse", nil
}

// TestAgenteObservadoRegistraSoloTrasResponder cierra el eslabón entre el
// adaptador y el recolector: la autoría se anota DESPUÉS de una respuesta con
// éxito, nunca antes ni tras un fallo.
func TestAgenteObservadoRegistraSoloTrasResponder(t *testing.T) {
	recolector := &recolectorAutoria{}
	fallido := &observedAgent{
		AuditorAgente: agenteQueFalla{adaptadorDe("claude", "opus", "xhigh")},
		authorship:    recolector,
	}
	if _, err := fallido.EjecutarPrompt("hola"); err == nil {
		t.Fatal("se esperaba error del agente")
	}
	if !recolector.consolidar().Vacio() {
		t.Error("atribuyó autoría a un agente que falló")
	}
}

func TestAgenteObservadoEjecutarRevisionRequiereCapacidadRestringida(t *testing.T) {
	soloPrompt := &agenteSoloPrompt{}
	agente := &observedAgent{
		AuditorAgente: soloPrompt,
		authorship:    &recolectorAutoria{},
	}

	_, err := agente.EjecutarRevision("revisar", "abc", []string{"a.go"})
	if err == nil {
		t.Fatal("EjecutarRevision debería fallar sin capacidad restringida")
	}
	if err.Error() != "semantic review unavailable" {
		t.Errorf("error = %q, esperado %q", err, "semantic review unavailable")
	}
	if soloPrompt.prompts != 0 {
		t.Errorf("EjecutarPrompt se ejecutó %d veces, esperado 0", soloPrompt.prompts)
	}
}

func TestAgenteObservadoForwardsSemanticToolPolicy(t *testing.T) {
	inner := &policyRecordingAgent{}
	agent := &observedAgent{AuditorAgente: inner, authorship: &recolectorAutoria{}}
	policy := reviewcontract.DefaultToolPolicy()

	if _, err := agent.ReviewWithPolicy("review", "abc", []string{"a.go"}, policy); err != nil {
		t.Fatalf("ReviewWithPolicy() error = %v", err)
	}
	if inner.policy != policy {
		t.Fatalf("policy = %#v, want %#v", inner.policy, policy)
	}
}

// reporteroEfectivo sabe decir quién atendió su última petición; es la
// capacidad que el motor necesita para sellar el producer de cada hallazgo.
type reporteroEfectivo struct {
	identidad agentadapter.AgenteEfectivo
	reporta   bool
}

func (r reporteroEfectivo) EjecutarPrompt(string) (string, error) { return "ok", nil }

func (r reporteroEfectivo) AgenteEfectivo() (agentadapter.AgenteEfectivo, bool) {
	return r.identidad, r.reporta
}

// TestAgenteObservadoReenviaElRespondedorEfectivo: el envoltorio debe exponer
// la identidad del hijo real; si no, los producers de los hallazgos quedan
// vacíos en la vía durable (defecto demostrado en la revisión en vivo de
// 9c68ef3).
func TestAgenteObservadoReenviaElRespondedorEfectivo(t *testing.T) {
	efectivo := agentadapter.AgenteEfectivo{Binario: "opencode", Modelo: "gpt-5.6-luna", Esfuerzo: "max"}
	envuelto := &observedAgent{
		AuditorAgente: reporteroEfectivo{identidad: efectivo, reporta: true},
		authorship:    &recolectorAutoria{},
	}
	obtenido, ok := envuelto.AgenteEfectivo()
	if !ok || obtenido != efectivo {
		t.Fatalf("AgenteEfectivo() = (%+v, %v), esperado (%+v, true)", obtenido, ok, efectivo)
	}

	mudo := &observedAgent{AuditorAgente: &agenteSoloPrompt{}, authorship: &recolectorAutoria{}}
	if _, ok := mudo.AgenteEfectivo(); ok {
		t.Fatal("un agente sin reporte no debería declarar identidad")
	}
}

// TestRecolectorRegistraElAgenteQueRespondio: el caso de H4. La ficha debe
// guardar quién respondió, no el nombre del perfil pedido.
func TestRecolectorRegistraElAgenteQueRespondio(t *testing.T) {
	recolector := &recolectorAutoria{}
	recolector.registrar(adaptadorDe("opencode", "sonnet", "high"))

	autoria := recolector.consolidar()
	if autoria.Binario != "opencode" {
		t.Errorf("binario = %q, esperado opencode", autoria.Binario)
	}
	if autoria.Modelo != "sonnet" || autoria.Esfuerzo != "high" {
		t.Errorf("modelo/esfuerzo = %q/%q, esperado sonnet/high", autoria.Modelo, autoria.Esfuerzo)
	}
}

// TestRecolectorSinAgentesNoInventaAutor: sin nadie que haya respondido, el
// campo queda vacío. Inventar un autor es exactamente el defecto de H4.
func TestRecolectorSinAgentesNoInventaAutor(t *testing.T) {
	recolector := &recolectorAutoria{}
	if !recolector.consolidar().Vacio() {
		t.Error("inventó un autor sin que ningún agente respondiera")
	}
}

// TestRecolectorConAgentesDistintosNoResumeUnoFalso: si cada dimensión la
// atendió un agente distinto, no hay un autor único que registrar. Elegir uno
// sería mentir con la misma forma que H4.
func TestRecolectorConAgentesDistintosNoResumeUnoFalso(t *testing.T) {
	recolector := &recolectorAutoria{}
	recolector.registrar(adaptadorDe("claude", "opus", "xhigh"))
	recolector.registrar(adaptadorDe("opencode", "sonnet", "high"))

	autoria := recolector.consolidar()
	if autoria.Binario == "claude" || autoria.Binario == "opencode" {
		t.Errorf("eligió un autor arbitrario entre agentes distintos: %+v", autoria)
	}
	if autoria.Binario != agentesMultiples {
		t.Errorf("binario = %q, esperado %q", autoria.Binario, agentesMultiples)
	}
}

// TestRecolectorConElMismoAgenteRepetidoSiConsolida: varias dimensiones
// atendidas por el mismo agente sí tienen un autor único.
func TestRecolectorConElMismoAgenteRepetidoSiConsolida(t *testing.T) {
	recolector := &recolectorAutoria{}
	recolector.registrar(adaptadorDe("claude", "opus", "xhigh"))
	recolector.registrar(adaptadorDe("claude", "opus", "xhigh"))

	if autoria := recolector.consolidar(); autoria.Binario != "claude" {
		t.Errorf("binario = %q, esperado claude", autoria.Binario)
	}
}
