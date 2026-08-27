package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/goslynn/awsacademycli/internal/canvas"
)

func at(s string) *time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestSuggestCourse(t *testing.T) {
	tests := []struct {
		name    string
		courses []canvas.Course
		want    int64
	}{
		{
			// El caso real: dos cursos con el mismo nombre, uno del término anterior.
			name: "prefiere el más nuevo",
			courses: []canvas.Course{
				{ID: 164446, Name: "AWS Academy Learner Lab", CreatedAt: at("2026-03-01")},
				{ID: 182613, Name: "AWS Academy Learner Lab", CreatedAt: at("2026-08-01")},
			},
			want: 182613,
		},
		{
			// Un curso terminado no sirve aunque sea el más reciente.
			name: "descarta los terminados",
			courses: []canvas.Course{
				{ID: 1, Name: "Viejo", CreatedAt: at("2026-01-01")},
				{ID: 2, Name: "Nuevo pero cerrado", CreatedAt: at("2026-08-01"), EndAt: at("2026-08-10")},
			},
			want: 1,
		},
		{
			name: "sin fechas, gana el id más alto",
			courses: []canvas.Course{
				{ID: 164446, Name: "AWS Academy Learner Lab"},
				{ID: 182613, Name: "AWS Academy Learner Lab"},
			},
			want: 182613,
		},
		{
			name:    "uno solo",
			courses: []canvas.Course{{ID: 42, Name: "Único"}},
			want:    42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.courses[suggestCourse(tt.courses)]
			if got.ID != tt.want {
				t.Errorf("sugerido = %d, esperaba %d", got.ID, tt.want)
			}
		})
	}
}

func TestCourseLabelDisambiguates(t *testing.T) {
	// Dos cursos homónimos tienen que producir etiquetas distintas: si no,
	// el usuario no tiene forma de elegir.
	a := canvas.Course{ID: 164446, Name: "AWS Academy Learner Lab", CreatedAt: at("2026-03-01")}
	b := canvas.Course{ID: 182613, Name: "AWS Academy Learner Lab", CreatedAt: at("2026-08-01")}

	if a.Label() == b.Label() {
		t.Fatalf("ambas etiquetas son %q; no se pueden distinguir", a.Label())
	}
	if !strings.Contains(a.Label(), "2026-03-01") {
		t.Errorf("la etiqueta debería incluir la fecha: %q", a.Label())
	}
}

func TestCourseEnded(t *testing.T) {
	past := canvas.Course{EndAt: at("2020-01-01")}
	if !past.Ended() {
		t.Error("un curso con fecha de cierre pasada debería contar como terminado")
	}
	open := canvas.Course{EndAt: at("2099-01-01")}
	if open.Ended() {
		t.Error("un curso con cierre futuro no está terminado")
	}
	unknown := canvas.Course{}
	if unknown.Ended() {
		t.Error("sin fechas no podemos afirmar que terminó")
	}
}
