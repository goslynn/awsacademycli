// Package atomicfile escribe ficheros de forma atómica.
//
// Los ficheros de estado se reescriben en cada operación; un escritor
// interrumpido a mitad de camino dejaría credenciales o cookies truncadas, y
// el siguiente arranque fallaría al parsearlas. Escribir a un temporal en el
// mismo directorio y renombrar evita ese estado intermedio.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write vuelca data en path con los permisos dados, sin estados intermedios visibles.
func Write(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
