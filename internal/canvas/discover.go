package canvas

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Course is a course the user is enrolled in.
type Course struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// CourseCode is the short name, useful for disambiguating on screen.
	CourseCode string `json:"course_code"`
	// AccessRestricted marks already-closed courses, which are of no use.
	AccessRestricted bool `json:"access_restricted_by_date"`

	// The fields that follow serve to tell apart courses with the same name:
	// AWS Academy calls them all "AWS Academy Learner Lab", so the name alone
	// is not enough to choose.
	CreatedAt *time.Time `json:"created_at"`
	StartAt   *time.Time `json:"start_at"`
	EndAt     *time.Time `json:"end_at"`
	Term      *struct {
		Name    string     `json:"name"`
		StartAt *time.Time `json:"start_at"`
		EndAt   *time.Time `json:"end_at"`
	} `json:"term"`
}

// Ended reports whether the course is over according to its end date.
func (c Course) Ended() bool {
	end := c.EndAt
	if end == nil && c.Term != nil {
		end = c.Term.EndAt
	}
	return end != nil && end.Before(time.Now())
}

// Label describes the course in one line, with what is needed to choose among
// several that share a name.
func (c Course) Label() string {
	var parts []string
	if c.Term != nil && c.Term.Name != "" {
		parts = append(parts, c.Term.Name)
	}
	if c.CreatedAt != nil {
		parts = append(parts, "created "+c.CreatedAt.Format("2006-01-02"))
	}
	if end := c.EndAt; end != nil {
		parts = append(parts, "ends "+end.Format("2006-01-02"))
	}
	if c.Ended() {
		parts = append(parts, "ENDED")
	}
	if len(parts) == 0 {
		return c.Name
	}
	return fmt.Sprintf("%s (%s)", c.Name, strings.Join(parts, ", "))
}

// Module is a course module.
type Module struct {
	ID    int64        `json:"id"`
	Name  string       `json:"name"`
	Items []ModuleItem `json:"items"`
}

// ModuleItem is an entry inside a module.
type ModuleItem struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	// Type is "ExternalTool" for the item that launches the lab.
	Type string `json:"type"`
	// ExternalURL points at the LTI provider, that is, at Vocareum.
	ExternalURL string `json:"external_url"`
	// HTMLURL is the Canvas page that triggers the launch.
	HTMLURL string `json:"html_url"`
}

// Courses lists the user's active courses.
func (c *Client) Courses(ctx context.Context) ([]Course, error) {
	var all []Course
	// include[]=term brings the term name, which is often the only thing that
	// tells two identically named courses apart.
	if err := c.getJSON(ctx,
		"/api/v1/courses?enrollment_state=active&include[]=term&per_page=100", &all); err != nil {
		return nil, err
	}
	// Expired courses still show up but do not let anything be opened.
	active := all[:0]
	for _, course := range all {
		if !course.AccessRestricted {
			active = append(active, course)
		}
	}
	return active, nil
}

// Modules lists the course modules with their items already included, so the
// lab can be resolved in a single call.
func (c *Client) Modules(ctx context.Context, courseID string) ([]Module, error) {
	path := fmt.Sprintf("/api/v1/courses/%s/modules?include[]=items&per_page=100", courseID)
	var modules []Module
	if err := c.getJSON(ctx, path, &modules); err != nil {
		return nil, err
	}
	return modules, nil
}

// Signals for recognising the item that launches the lab among the rest.
//
// The title is not enough: in a typical course nearly every item mentions the
// learner lab — the guide, the demos, the FAQ — and only one actually launches
// it. What does distinguish them is which LTI provider each one points at.
//
// The title patterns keep their Spanish alternatives alongside the English
// ones: Canvas serves item titles in the account's own language, so these match
// remote data rather than text this repository owns.
var (
	labProviderHost = regexp.MustCompile(`(?i)vocareum`)
	labActionTitle  = regexp.MustCompile(`(?i)\b(iniciar|start|launch|abrir|open)\b`)
	labSubjectTitle = regexp.MustCompile(`(?i)(learner\s*lab|laboratorio de aprendizaje|learning\s*lab)`)
	// Material *about* the lab, which does not launch it.
	labAboutTitle = regexp.MustCompile(`(?i)\b(gu[íi]a|guide|demostraci[óo]n|demonstration|demo|` +
		`preguntas frecuentes|frequently asked questions|faq|c[óo]mo|how\s*to|encuesta|survey|` +
		`evaluaci[óo]n|assessment|recursos|resources|introducci[óo]n|introduction)\b`)
)

// scoreLabItem scores how likely it is that an item launches the lab.
func scoreLabItem(it ModuleItem) int {
	score := 0
	// The provider weighs more than anything else: it is the one hosting the
	// lab, whereas the rest of the material lives elsewhere.
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

// LabItem is the module item that launches the lab.
type LabItem struct {
	CourseID   string
	CourseName string
	ItemID     string
	Title      string
	// LaunchURL is the Canvas page to visit in order to trigger the LTI launch.
	LaunchURL string
}

// FindLabItem locates the item that launches the Learner Lab within a course.
//
// We never hardcode identifiers: the course changes every term and all the URLs
// change with it, so it is resolved through the API on every discovery.
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
		return nil, fmt.Errorf("course %q has no external tool item", course.Name)
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

	// Choosing wrong means launching the wrong tool, so on a tie or with only
	// negative signals we would rather fail while saying what we found, instead
	// of guessing.
	if bestScore <= 0 || tied {
		var titles []string
		for _, it := range external {
			titles = append(titles, fmt.Sprintf("%q -> %s", it.Title, it.ExternalURL))
		}
		return nil, fmt.Errorf(
			"could not tell which of these items launches the lab:\n  %s",
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

// CourseByID retrieves a specific course, for when the user pinned it by hand.
func (c *Client) CourseByID(ctx context.Context, id string) (*Course, error) {
	var course Course
	if err := c.getJSON(ctx, "/api/v1/courses/"+id, &course); err != nil {
		return nil, err
	}
	return &course, nil
}
