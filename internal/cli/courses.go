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
		Short: "List your courses and choose which one has the lab",
		Long: `Shows the active courses in your AWS Academy account.

AWS Academy calls them all "AWS Academy Learner Lab", so the term, the creation
date and the end date are listed too, which is what makes them
distinguishable. The one you want is usually the most recent one that has not
ended.

To pin one:

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
	cmd.Flags().StringVar(&use, "use", "", "pin this course in the configuration")
	return cmd
}

// setCourse saves the chosen course and locates the lab again.
func setCourse(ctx context.Context, app *App, id string) error {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return fmt.Errorf("%q is not a course id", id)
	}
	if _, err := app.EnsureSession(ctx); err != nil {
		return err
	}
	course, err := app.canvas.CourseByID(ctx, id)
	if err != nil {
		return fmt.Errorf("could not open course %s: %w", id, err)
	}

	app.cfg.CourseID = id
	if err := app.cfg.Save(); err != nil {
		return err
	}

	// The cached lab points at the previous course, so we look it up again now
	// and not in the middle of the next 'start'.
	disc, err := app.Discover(ctx)
	if err != nil {
		return err
	}

	if flagJSON {
		return printJSON(map[string]any{
			"course_id": id, "course": course.Name, "item": disc.ItemTitle,
		})
	}
	fmt.Printf("%s course pinned: %s\n", mark(true), course.Label())
	fmt.Printf("  lab:      %s\n", disc.ItemTitle)
	fmt.Printf("  saved in: %s\n", config.Path())
	return nil
}

func printCourses(courses []canvas.Course, current string) {
	if len(courses) == 0 {
		fmt.Println("There are no active courses in your account.")
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
		fmt.Printf("\nNone pinned. Pick one with:\n  awsacademy courses --use <id>\n")
	}
}

// chooseCourse asks which course to use when there is more than one.
func chooseCourse(ctx context.Context, courses []canvas.Course) (*canvas.Course, error) {
	options := make([]ui.Option, len(courses))
	for i, c := range courses {
		options[i] = ui.Option{
			Label: fmt.Sprintf("%-8d %s", c.ID, c.Name),
			Hint:  courseHint(c),
		}
	}

	// The most recent one still alive is suggested, which is almost always the
	// right one, but the person makes the call: choosing wrong means bringing
	// up the wrong lab.
	idx, err := ui.Select(ctx, "\nWhich of your courses has the lab?",
		options, suggestCourse(courses))
	if err != nil {
		return nil, err
	}
	return &courses[idx], nil
}

// courseHint is what tells two courses with the same name apart.
func courseHint(c canvas.Course) string {
	var parts []string
	if c.Term != nil && c.Term.Name != "" {
		parts = append(parts, c.Term.Name)
	}
	if c.CreatedAt != nil {
		parts = append(parts, "created "+c.CreatedAt.Format("2006-01-02"))
	}
	if c.EndAt != nil {
		parts = append(parts, "ends "+c.EndAt.Format("2006-01-02"))
	}
	if c.Ended() {
		parts = append(parts, "ENDED")
	}
	return strings.Join(parts, ", ")
}

// suggestCourse returns the index of the likeliest candidate: the most recently
// created one among those that have not ended.
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
			// Without dates, a higher id means a newer course.
			best = i
		}
	}
	return best
}
