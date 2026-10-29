package spimi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	currentFile      = "CURRENT"
	generationPrefix = "gen-"
)

// activeIndexDir resuelve una generación completa a partir de CURRENT.
// Sin CURRENT, abre el formato anterior de tres archivos en la raíz.
func activeIndexDir(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, currentFile))
	if errors.Is(err, os.ErrNotExist) {
		return root, nil
	}
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(raw))
	if !validGeneration(name) {
		return "", fmt.Errorf("%w: CURRENT contiene una generación inválida", ErrCorruptIndex)
	}
	dir := filepath.Join(root, name)
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("%w: generación %s: %v", ErrCorruptIndex, name, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s no es directorio", ErrCorruptIndex, name)
	}
	return dir, nil
}

func validGeneration(name string) bool {
	return strings.HasPrefix(name, generationPrefix) &&
		len(name) > len(generationPrefix) &&
		filepath.Base(name) == name &&
		!strings.ContainsAny(name, `/\`) &&
		!strings.Contains(name, "..")
}

// publishGeneration publica una referencia pequeña, una vez que todos los
// archivos de la nueva generación están cerrados y sincronizados.
// No modifica los archivos de generaciones usadas por lectores existentes.
func publishGeneration(root, name string) error {
	if !validGeneration(name) {
		return fmt.Errorf("spimi: nombre de generación inválido: %q", name)
	}
	tmp, err := os.CreateTemp(root, "spimi-current-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.WriteString(name + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), filepath.Join(root, currentFile)); err != nil {
		return fmt.Errorf("spimi: publicar CURRENT: %w", err)
	}
	return nil
}
