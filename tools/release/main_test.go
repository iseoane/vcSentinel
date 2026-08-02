package main

import "testing"

func TestVersionMenorOIgual(t *testing.T) {
	tests := []struct {
		nombre    string
		nueva     string
		publicada string
		esperado  bool
	}{
		{"inferior mayor bloquea", "0.2.0", "0.3.0", true},
		{"igual bloquea", "0.1.0", "0.1.0", true},
		{"superior menor permite", "0.3.0", "0.2.0", false},
		{"inferior menor bloquea", "0.1.5", "0.2.0", true},
		{"igual menor bloquea", "0.1.0", "0.1.0", true},
		{"superior patch permite", "0.1.1", "0.1.0", false},
		{"con prefijo v se normaliza", "v0.2.0", "0.1.0", false},
		{"publicada con v se normaliza", "0.1.0", "v0.2.0", true},
		{"componentes cortos se rellenan con cero", "1.2", "1.2.0", true},
		{"componentes cortos superiores permiten", "1.3", "1.2.9", false},
		{"sufijo no numérico cuenta como cero", "0.1.0-beta", "0.1.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			got := versionMenorOIgual(tt.nueva, tt.publicada)
			if got != tt.esperado {
				t.Errorf("versionMenorOIgual(%q, %q) = %v, esperado %v", tt.nueva, tt.publicada, got, tt.esperado)
			}
		})
	}
}
