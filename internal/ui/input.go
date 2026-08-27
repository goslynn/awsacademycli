package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Las lecturas de stdin son bloqueantes y no atienden a la cancelación del
// contexto, así que cada una se hace en su propia goroutine y se compite contra
// ctx.Done(). Sin esto, un Ctrl+C durante una pregunta no haría nada: el
// programa instala su propio manejador de SIGINT —lo que desactiva la
// terminación por defecto— y se quedaría esperando una línea para siempre.
//
// La goroutine que quedó leyendo se abandona a propósito: cancelar significa
// terminar, así que nadie va a volver a usar esa entrada.

// Prompt pide una línea de texto, respetando la cancelación.
func Prompt(ctx Context, in io.Reader, label string) (string, error) {
	fmt.Fprint(os.Stderr, label)

	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(in).ReadString('\n')
		ch <- result{line, err}
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr)
		return "", ErrCancelled
	case r := <-ch:
		if r.err != nil && r.line == "" {
			if errors.Is(r.err, io.EOF) {
				// Ctrl+D en una pregunta es abandonar, no un fallo.
				fmt.Fprintln(os.Stderr)
				return "", ErrCancelled
			}
			return "", r.err
		}
		return strings.TrimSpace(r.line), nil
	}
}

// PromptPassword pide una contraseña sin eco, respetando la cancelación.
func PromptPassword(ctx Context, label string) (string, error) {
	fmt.Fprint(os.Stderr, label)

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		fmt.Fprintln(os.Stderr)
		return Prompt(ctx, os.Stdin, "")
	}

	// Se guarda el estado para poder devolver el eco si cancelamos: dejar el
	// terminal mudo al salir obligaría a la persona a ejecutar `reset`.
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}

	type result struct {
		raw []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		raw, err := term.ReadPassword(fd)
		ch <- result{raw, err}
	}()

	select {
	case <-ctx.Done():
		_ = term.Restore(fd, state)
		fmt.Fprintln(os.Stderr)
		return "", ErrCancelled
	case r := <-ch:
		fmt.Fprintln(os.Stderr)
		if r.err != nil {
			return "", r.err
		}
		return strings.TrimSpace(string(r.raw)), nil
	}
}

// Confirm hace una pregunta de sí o no. defaultYes decide qué significa un
// enter vacío.
func Confirm(ctx Context, in io.Reader, label string, defaultYes bool) (bool, error) {
	answer, err := Prompt(ctx, in, label)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "":
		return defaultYes, nil
	case "s", "si", "sí", "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// Context es lo único que necesitamos de context.Context. Declararlo así
// mantiene el paquete centrado en la terminal y hace triviales los tests.
type Context interface {
	Done() <-chan struct{}
}
