package canvas

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Course es un curso en el que está matriculado el usuario.
type Course struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// CourseCode es el nombre corto, útil para desambiguar en pantalla.
	CourseCode string `json:"course_code"`
	// AccessRestricted marca cursos ya cerrados, que no sirven.
	AccessRestricted bool `json:"access_restricted_by_date"`

	// Los campos que siguen sirven para distinguir cursos homónimos: AWS
	// Academy los llama a todos "AWS Academy Learner Lab", así que el nombre
	// por sí solo no alcanza para elegir.
	CreatedAt *time.Time `json:"created_at"`
	StartAt   *time.Time `json:"start_at"`
	EndAt     *time.Time `json:"end_at"`
	Term      *struct {
		Name    string     `json:"name"`
		StartAt *time.Time `json:"start_at"`
		EndAt   *time.Time `json:"end_at"`
	} `json:"term"`
}

// Ended indica si el curso ya terminó según su fecha de cierre.
func (c Course) Ended() bool {
	end := c.EndAt
	if end == nil && c.Term != nil {
		end = c.Term.EndAt
	}
	return end != nil && end.Before(time.Now())
}

// Label describe el curso en una línea, con lo necesario para elegir entre
// varios que se llaman igual.
func (c Course) Label() string {
	var parts []string
	if c.Term != nil && c.Term.Name != "" {
		parts = append(parts, c.Term.Name)
	}
	if c.CreatedAt != nil {
		parts = append(parts, "creado "+c.CreatedAt.Format("2006-01-02"))
	}
	if end := c.EndAt; end != nil {
		parts = append(parts, "termina "+end.Format("2006-01-02"))
	}
	if c.Ended() {
		parts = append(parts, "TERMINADO")
	}
	if len(parts) == 0 {
		return c.Name
	}
	return fmt.Sprintf("%s (%s)", c.Name, strings.Join(parts, ", "))
}

// Module es un módulo del curso.
type Module struct {
	ID    int64        `json:"id"`
	Name  string       `json:"name"`
	Items []ModuleItem `json:"items"`
}

// ModuleItem es una entrada dentro de un módulo.
type ModuleItem struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	// Type vale "ExternalTool" para el ítem que lanza el laboratorio.
	Type string `json:"type"`
	// ExternalURL apunta al proveedor LTI, es decir a Vocareum.
	ExternalURL string `json:"external_url"`
	// HTMLURL es la página de Canvas que dispara el lanzamiento.
	HTMLURL string `json:"html_url"`
}

// Courses lista los cursos activos del usuario.
func (c *Client) Courses(ctx context.Context) ([]Course, error) {
	var all []Course
	// include[]=term trae el nombre del período lectivo, que suele ser lo
	// único que distingue dos cursos homónimos.
	if err := c.getJSON(ctx,
		"/api/v1/courses?enrollment_state=active&include[]=term&per_page=100", &all); err != nil {
		return nil, err
	}
	// Los cursos vencidos siguen apareciendo pero no dejan abrir nada.
	active := all[:0]
	for _, course := range all {
		if !course.AccessRestricted {
			active = append(active, course)
		}
	}
	return active, nil
}

// Modules lista los módulos del curso con sus ítems ya incluidos, para
// resolver el laboratorio en una sola llamada.
func (c *Client) Modules(ctx context.Context, courseID string) ([]Module, error) {
	path := fmt.Sprintf("/api/v1/courses/%s/modules?include[]=items&per_page=100", courseID)
	var modules []Module
	if err := c.getJSON(ctx, path, &modules); err != nil {
		return nil, err
	}
	return modules, nil
}

// Señales para reconocer el ítem que lanza el laboratorio entre los demás.
//
// El título no alcanza: en un curso típico casi todos los ítems mencionan el
// "Laboratorio de aprendizaje" —la guía, las demostraciones, las preguntas
// frecuentes— y solo uno lo lanza de verdad. Lo que sí distingue es a qué
// proveedor LTI apunta cada uno.
var (
	labProviderHost = regexp.MustCompile(`(?i)vocareum`)
	labActionTitle  = regexp.MustCompile(`(?i)\b(iniciar|start|launch|abrir)\b`)
	labSubjectTitle = regexp.MustCompile(`(?i)(learner\s*lab|laboratorio de aprendizaje|learning\s*lab)`)
	// Material *sobre* el laboratorio, que no lo lanza.
	labAboutTitle = regexp.MustCompile(`(?i)\b(gu[íi]a|guide|demostraci[óo]n|demo|preguntas frecuentes|faq|` +
		`c[óo]mo|how\s*to|encuesta|survey|evaluaci[óo]n|recursos|introducci[óo]n)\b`)
)

// scoreLabItem puntúa lo probable que es que un ítem lance el laboratorio.
func scoreLabItem(it ModuleItem) int {
	score := 0
	// El proveedor pesa más que cualquier otra cosa: es el que aloja el
	// laboratorio, mientras que el resto del material vive en otro sitio.
	if labProviderHost.MatchString(it.ExternalURL) {
		score += 100
	}
	if labActionTitle.MatchString(it.Title) {
		score += 10
	}
	if labSubjectTitle.MatchString(it.Title) {
		score += 5
	}
	if labAboutTitle.MatchString(it.Title) {
		score -= 50
	}
	return score
}

// LabItem es el ítem de módulo que lanza el laboratorio.
type LabItem struct {
	CourseID   string
	CourseName string
	ItemID     string
	Title      string
	// LaunchURL es la página de Canvas a la que hay que ir para disparar el LTI.
	LaunchURL string
}

// FindLabItem localiza el ítem que lanza el Learner Lab dentro de un curso.
//
// Nunca hardcodeamos identificadores: el curso cambia cada término y con él
// cambian todas las URLs, así que se resuelve por API en cada descubrimiento.
func (c *Client) FindLabItem(ctx context.Context, course Course) (*LabItem, error) {
	courseID := strconv.FormatInt(course.ID, 10)
	modules, err := c.Modules(ctx, courseID)
	if err != nil {
		return nil, err
	}

	var external []ModuleItem
	for _, module := range modules {
		for _, item := range module.Items {
			if item.Type == "ExternalTool" {
				external = append(external, item)
			}
		}
	}
	if len(external) == 0 {
		return nil, fmt.Errorf("el curso %q no tiene ningún ítem de herramienta externa", course.Name)
	}

	best, bestScore, tied := -1, 0, false
	for i, item := range external {
		switch score := scoreLabItem(item); {
		case best == -1 || score > bestScore:
			best, bestScore, tied = i, score, false
		case score == bestScore:
			tied = true
		}
	}

	// Elegir mal significa lanzar la herramienta equivocada, así que ante un
	// empate o ante señales solo negativas preferimos fallar diciendo qué se
	// encontró, en vez de adivinar.
	if bestScore <= 0 || tied {
		var titles []string
		for _, it := range external {
			titles = append(titles, fmt.Sprintf("%q -> %s", it.Title, it.ExternalURL))
		}
		return nil, fmt.Errorf(
			"no supe cuál de estos ítems lanza el laboratorio:\n  %s",
			strings.Join(titles, "\n  "))
	}
	chosen := external[best]

	launch := chosen.HTMLURL
	if launch == "" {
		launch = fmt.Sprintf("%s/courses/%s/modules/items/%d", c.baseURL, courseID, chosen.ID)
	}

	return &LabItem{
		CourseID:   courseID,
		CourseName: course.Name,
		ItemID:     strconv.FormatInt(chosen.ID, 10),
		Title:      strings.TrimSpace(chosen.Title),
		LaunchURL:  launch,
	}, nil
}

// CourseByID recupera un curso concreto, para cuando el usuario lo fijó a mano.
func (c *Client) CourseByID(ctx context.Context, id string) (*Course, error) {
	var course Course
	if err := c.getJSON(ctx, "/api/v1/courses/"+id, &course); err != nil {
		return nil, err
	}
	return &course, nil
}
