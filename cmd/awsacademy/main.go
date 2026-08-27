// Command awsacademy controla el AWS Academy Learner Lab desde la terminal.
//
// Levanta y baja el laboratorio y mantiene el perfil de AWS CLI con
// credenciales frescas, haciendo por debajo el mismo recorrido que haría una
// persona con un navegador: login en Canvas, lanzamiento LTI y control del
// laboratorio en Vocareum.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/goslynn/awsacademycli/internal/cli"
)

// version la fija el linker en las builds de release:
//
//	go build -ldflags "-X main.version=$(git describe --tags)" ./cmd/awsacademy
var version = "dev"

func main() {
	// El primer Ctrl+C cancela el trabajo en curso, que puede ser una espera de
	// varios minutos, y deja que la herramienta salga ordenadamente.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Instalar un manejador desactiva la terminación por defecto, así que si
	// algo no atendiera la cancelación el proceso quedaría colgado e
	// inmatable con Ctrl+C. Devolver el comportamiento normal en cuanto llega
	// la primera señal hace que un segundo Ctrl+C siempre funcione.
	go func() {
		<-ctx.Done()
		stop()
	}()

	os.Exit(cli.ExecuteContext(ctx, version))
}
