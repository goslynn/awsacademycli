package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// bloqueante simula una entrada que nunca entrega datos, como un terminal
// esperando a que alguien teclee.
type bloqueante struct{}

func (bloqueante) Read([]byte) (int, error) {
	select {} // nunca retorna
}

func TestPromptRespondsToCancellation(t *testing.T) {
	// Este es el fallo que motivó todo esto: sin atender al contexto, un
	// Ctrl+C durante una pregunta dejaba el proceso colgado para siempre,
	// porque instalar un manejador de señales desactiva la terminación
	// por defecto.
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := Prompt(ctx, bloqueante{}, "Email: ")
		done <- err
	}()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrCancelled) {
			t.Errorf("err = %v, esperaba ErrCancelled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt no atendió la cancelación: seguiría colgado")
	}
}

func TestPromptReadsLine(t *testing.T) {
	got, err := Prompt(context.Background(), strings.NewReader("ada@example.com\n"), "Email: ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ada@example.com" {
		t.Errorf("= %q", got)
	}
}

func TestPromptTrimsWhitespace(t *testing.T) {
	got, err := Prompt(context.Background(), strings.NewReader("  ada@example.com  \n"), "Email: ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ada@example.com" {
		t.Errorf("= %q, esperaba sin espacios sobrantes", got)
	}
}

func TestPromptTreatsEOFAsCancellation(t *testing.T) {
	// Ctrl+D en una pregunta es abandonar, no un error que reportar.
	_, err := Prompt(context.Background(), strings.NewReader(""), "Email: ")
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, esperaba ErrCancelled", err)
	}
}

func TestConfirmDefaults(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		defaultYes bool
		want       bool
	}{
		{"enter con defecto sí", "\n", true, true},
		{"enter con defecto no", "\n", false, false},
		{"s", "s\n", false, true},
		{"si", "si\n", false, true},
		{"sí con acento", "sí\n", false, true},
		{"y", "y\n", false, true},
		{"mayúscula", "S\n", false, true},
		{"n", "n\n", true, false},
		{"cualquier otra cosa es no", "quizás\n", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Confirm(context.Background(), strings.NewReader(tt.input), "¿sí? ", tt.defaultYes)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("= %v, esperaba %v", got, tt.want)
			}
		})
	}
}

func TestConfirmPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Confirm(ctx, bloqueante{}, "¿sí? ", false)
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, esperaba ErrCancelled", err)
	}
}

func TestSelectNumberedRespondsToCancellation(t *testing.T) {
	// La variante sin terminal también lee stdin de forma bloqueante.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := selectNumbered(ctx, "¿cuál?", []Option{{Label: "a"}, {Label: "b"}}, 0)
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, esperaba ErrCancelled", err)
	}
}

var _ io.Reader = bloqueante{}
