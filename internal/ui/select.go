// Package ui son las interacciones de terminal que no encajan en un flag.
package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"golang.org/x/term"
)

// ErrCancelled indica que la persona abandonó la selección.
var ErrCancelled = errors.New("selección cancelada")

// Option es una entrada del menú.
type Option struct {
	// Label es la línea principal.
	Label string
	// Hint es el detalle que ayuda a decidir; se muestra atenuado.
	Hint string
}

// Select muestra un menú y devuelve el índice elegido.
//
// Con terminal se navega con las flechas y se confirma con Enter. Sin ella
// —una tubería, un script, un test— cae en una lista numerada, porque el modo
// crudo requiere un terminal de verdad y no queremos que la herramienta se
// vuelva inusable fuera de una sesión interactiva.
func Select(ctx Context, prompt string, options []Option, defaultIdx int) (int, error) {
	if len(options) == 0 {
		return 0, errors.New("no hay opciones que elegir")
	}
	if defaultIdx < 0 || defaultIdx >= len(options) {
		defaultIdx = 0
	}
	if len(options) == 1 {
		return 0, nil
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return selectNumbered(ctx, prompt, options, defaultIdx)
	}
	return selectInteractive(ctx, fd, prompt, options, defaultIdx)
}

func selectInteractive(ctx Context, fd int, prompt string, options []Option, cursor int) (int, error) {
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return selectNumbered(ctx, prompt, options, cursor)
	}
	// El terminal queda inservible si salimos sin restaurarlo, incluso ante un
	// error o un pánico, así que se restaura pase lo que pase.
	defer term.Restore(fd, oldState)

	out := os.Stderr
	fmt.Fprintf(out, "%s\r\n", prompt)
	fmt.Fprint(out, "\x1b[?25l")       // ocultar el cursor mientras navegamos
	defer fmt.Fprint(out, "\x1b[?25h") // y devolverlo al terminar

	render(out, options, cursor)

	// Repintar es siempre lo mismo: volver al principio del bloque y volver a
	// dibujarlo encima. Hacerlo sin subir primero deja una copia de la lista
	// debajo de la anterior.
	repaint := func() {
		fmt.Fprintf(out, "\x1b[%dA", len(options))
		render(out, options, cursor)
	}

	// El modo crudo desactiva ISIG, así que Ctrl+C no llega como señal sino
	// como el byte 3; se atiende abajo. El contexto se vigila igualmente por si
	// la cancelación viene de otro sitio.
	type keypress struct {
		buf []byte
		n   int
		err error
	}
	keys := make(chan keypress, 1)
	go func() {
		for {
			buf := make([]byte, 3)
			n, err := os.Stdin.Read(buf)
			keys <- keypress{buf, n, err}
			if err != nil {
				return
			}
		}
	}()

	for {
		var buf []byte
		var n int
		select {
		case <-ctx.Done():
			return 0, ErrCancelled
		case k := <-keys:
			if k.err != nil {
				return 0, k.err
			}
			buf, n = k.buf, k.n
		}

		switch {
		case n == 1 && (buf[0] == '\r' || buf[0] == '\n'):
			repaint()
			return cursor, nil

		case n == 1 && (buf[0] == 3 || buf[0] == 'q'): // Ctrl-C o q
			repaint()
			return 0, ErrCancelled

		case n == 1 && buf[0] == 'k', n == 3 && buf[0] == 0x1b && buf[2] == 'A':
			cursor = (cursor - 1 + len(options)) % len(options)

		case n == 1 && buf[0] == 'j', n == 3 && buf[0] == 0x1b && buf[2] == 'B':
			cursor = (cursor + 1) % len(options)

		case n == 1 && buf[0] >= '1' && buf[0] <= '9':
			// Un atajo: teclear el número salta directamente a esa opción.
			if idx := int(buf[0] - '1'); idx < len(options) {
				cursor = idx
			}
		}
		repaint()
	}
}

// render dibuja la lista con la opción actual marcada.
func render(out io.Writer, options []Option, cursor int) {
	for i, opt := range options {
		// \x1b[K borra el resto de la línea: sin eso, una etiqueta corta
		// dejaría a la vista el final de la anterior, más larga.
		if i == cursor {
			fmt.Fprintf(out, "\x1b[K  \x1b[1;36m❯ %s\x1b[0m", opt.Label)
		} else {
			fmt.Fprintf(out, "\x1b[K    %s", opt.Label)
		}
		if opt.Hint != "" {
			fmt.Fprintf(out, "  \x1b[2m%s\x1b[0m", opt.Hint)
		}
		fmt.Fprint(out, "\r\n")
	}
}

// selectNumbered es la variante para cuando no hay terminal interactivo.
func selectNumbered(ctx Context, prompt string, options []Option, defaultIdx int) (int, error) {
	fmt.Fprintf(os.Stderr, "%s\n", prompt)
	for i, opt := range options {
		line := fmt.Sprintf("  %d) %s", i+1, opt.Label)
		if opt.Hint != "" {
			line += "  " + opt.Hint
		}
		fmt.Fprintln(os.Stderr, line)
	}
	answer, err := Prompt(ctx, os.Stdin,
		fmt.Sprintf("\nElegí [1-%d, enter = %d]: ", len(options), defaultIdx+1))
	if err != nil {
		return 0, err
	}
	if answer == "" {
		return defaultIdx, nil
	}
	n, err := strconv.Atoi(answer)
	if err != nil || n < 1 || n > len(options) {
		return 0, fmt.Errorf("elegí un número entre 1 y %d", len(options))
	}
	return n - 1, nil
}
