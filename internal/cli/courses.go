package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/goslynn/awsacademycli/internal/canvas"
	"github.com/goslynn/awsacademycli/internal/config"
	"github.com/goslynn/awsacademycli/internal/ui"
	"github.com/spf13/cobra"
)

func newCoursesCmd() *cobra.Command {
	var use string
	cmd := &cobra.Command{
		Use:   "courses",
		Short: "Lista tus cursos y elige cuál tiene el laboratorio",
		Long: `Muestra los cursos activos de tu cuenta de AWS Academy.

AWS Academy los llama a todos "AWS Academy Learner Lab", así que se listan
también el período, la fecha de creación y la de cierre, que es lo que permite
distinguirlos. Normalmente el que te interesa es el más reciente que no haya
terminado.

Para fijar uno:

  awsacademy courses --use 182613`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(flagDebugHTTP)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			if use != "" {
				return setCourse(ctx, app, use)
			}

			if _, err := app.EnsureSession(ctx); err != nil {
				return err
			}
			courses, err := app.canvas.Courses(ctx)
			if err != nil {
				return err
			}
			if flagJSON {
				return printJSON(courses)
			}
			printCourses(courses, app.cfg.CourseID)
			return nil
		},
	}
	cmd.Flags().StringVar(&use, "use", "", "fijar este curso en la configuración")
	return cmd
}

// setCourse guarda el curso elegido y vuelve a localizar el laboratorio.
func setCourse(ctx context.Context, app *App, id string) error {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return fmt.Errorf("%q no es un id de curso", id)
	}
	if _, err := app.EnsureSession(ctx); err != nil {
		return err
	}
	course, err := app.canvas.CourseByID(ctx, id)
	if err != nil {
		return fmt.Errorf("no pude abrir el curso %s: %w", id, err)
	}

	app.cfg.CourseID = id
	if err := app.cfg.Save(); err != nil {
		return err
	}

	// El laboratorio cacheado apunta al curso anterior, así que se vuelve a
	// buscar ahora y no en mitad del próximo 'start'.
	disc, err := app.Discover(ctx)
	if err != nil {
		return err
	}

	if flagJSON {
		return printJSON(map[string]any{
			"course_id": id, "course": course.Name, "item": disc.ItemTitle,
		})
	}
	fmt.Printf("%s curso fijado: %s\n", mark(true), course.Label())
	fmt.Printf("  laboratorio: %s\n", disc.ItemTitle)
	fmt.Printf("  guardado en: %s\n", config.Path())
	return nil
}

func printCourses(courses []canvas.Course, current string) {
	if len(courses) == 0 {
		fmt.Println("No hay cursos activos en tu cuenta.")
		return
	}
	for _, c := range courses {
		id := strconv.FormatInt(c.ID, 10)
		marker := " "
		if id == current {
			marker = "*"
		}
		fmt.Printf("%s %-8s %s\n", marker, id, c.Label())
	}
	if current == "" {
		fmt.Printf("\nNinguno fijado. Elegí uno con:\n  awsacademy courses --use <id>\n")
	}
}

// chooseCourse pregunta cuál curso usar cuando hay más de uno.
func chooseCourse(ctx context.Context, courses []canvas.Course) (*canvas.Course, error) {
	options := make([]ui.Option, len(courses))
	for i, c := range courses {
		options[i] = ui.Option{
			Label: fmt.Sprintf("%-8d %s", c.ID, c.Name),
			Hint:  courseHint(c),
		}
	}

	// Se sugiere el más reciente que siga vivo, que es casi siempre el bueno,
	// pero la decisión la toma la persona: elegir mal significa levantar el
	// laboratorio equivocado.
	idx, err := ui.Select(ctx, "\n¿Cuál de tus cursos tiene el laboratorio?",
		options, suggestCourse(courses))
	if err != nil {
		return nil, err
	}
	return &courses[idx], nil
}

// courseHint es lo que distingue dos cursos con el mismo nombre.
func courseHint(c canvas.Course) string {
	var parts []string
	if c.Term != nil && c.Term.Name != "" {
		parts = append(parts, c.Term.Name)
	}
	if c.CreatedAt != nil {
		parts = append(parts, "creado "+c.CreatedAt.Format("2006-01-02"))
	}
	if c.EndAt != nil {
		parts = append(parts, "termina "+c.EndAt.Format("2006-01-02"))
	}
	if c.Ended() {
		parts = append(parts, "TERMINADO")
	}
	return strings.Join(parts, ", ")
}

// suggestCourse devuelve el índice del candidato más probable: el creado más
// recientemente entre los que no han terminado.
func suggestCourse(courses []canvas.Course) int {
	best := 0
	for i, c := range courses {
		if c.Ended() && !courses[best].Ended() {
			continue
		}
		if !c.Ended() && courses[best].Ended() {
			best = i
			continue
		}
		if c.CreatedAt != nil && courses[best].CreatedAt != nil &&
			c.CreatedAt.After(*courses[best].CreatedAt) {
			best = i
		} else if c.ID > courses[best].ID && c.CreatedAt == nil {
			// Sin fechas, un id más alto es un curso más nuevo.
			best = i
		}
	}
	return best
}
