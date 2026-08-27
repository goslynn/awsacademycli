package ui

import (
	"context"
	"strings"
	"testing"
)

func TestRenderEmitsOneLinePerOption(t *testing.T) {
	// Este es el invariante que sostiene el repintado: se vuelve arriba con
	// \x1b[NA, donde N es el número de opciones. Si render emitiera un número
	// distinto de líneas, cada repintado dejaría una copia de la lista.
	options := []Option{
		{Label: "164446   AWS Academy Learner Lab", Hint: "creado 2026-03-15"},
		{Label: "182613   AWS Academy Learner Lab", Hint: "creado 2026-08-10"},
		{Label: "999999   Otro curso"},
	}

	var sb strings.Builder
	render(&sb, options, 1)

	if got := strings.Count(sb.String(), "\r\n"); got != len(options) {
		t.Errorf("render emitió %d saltos de línea, esperaba %d", got, len(options))
	}
}

func TestRenderMarksOnlyTheCursor(t *testing.T) {
	options := []Option{{Label: "uno"}, {Label: "dos"}, {Label: "tres"}}

	var sb strings.Builder
	render(&sb, options, 2)
	out := sb.String()

	if got := strings.Count(out, "❯"); got != 1 {
		t.Errorf("hay %d marcadores, esperaba exactamente 1", got)
	}
	// El marcador tiene que estar en la línea del cursor, no en otra.
	lines := strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n")
	if len(lines) != 3 {
		t.Fatalf("esperaba 3 líneas, hay %d", len(lines))
	}
	if !strings.Contains(lines[2], "❯") || !strings.Contains(lines[2], "tres") {
		t.Errorf("el marcador no está en la opción seleccionada: %q", lines[2])
	}
}

func TestRenderClearsEachLine(t *testing.T) {
	// Sin el borrado de línea, pasar de una etiqueta larga a una corta dejaría
	// visible la cola de la anterior.
	options := []Option{{Label: "una etiqueta bastante larga"}, {Label: "corta"}}

	var sb strings.Builder
	render(&sb, options, 0)

	if got := strings.Count(sb.String(), "\x1b[K"); got != len(options) {
		t.Errorf("hay %d borrados de línea, esperaba uno por opción (%d)", got, len(options))
	}
}

func TestSelectSingleOptionDoesNotPrompt(t *testing.T) {
	// Con una sola opción no hay nada que elegir: preguntar sería ruido.
	idx, err := Select(context.Background(), "¿cuál?", []Option{{Label: "único"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if idx != 0 {
		t.Errorf("idx = %d", idx)
	}
}

func TestSelectRejectsEmptyOptions(t *testing.T) {
	if _, err := Select(context.Background(), "¿cuál?", nil, 0); err == nil {
		t.Error("esperaba un error sin opciones")
	}
}
