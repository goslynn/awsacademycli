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
			// The real case: two courses with the same name, one from the
			// previous term.
			name: "prefers the newer one",
			courses: []canvas.Course{
				{ID: 164446, Name: "AWS Academy Learner Lab", CreatedAt: at("2026-03-01")},
				{ID: 182613, Name: "AWS Academy Learner Lab", CreatedAt: at("2026-08-01")},
			},
			want: 182613,
		},
		{
			// An ended course is of no use even if it is the most recent one.
			name: "discards the ended ones",
			courses: []canvas.Course{
				{ID: 1, Name: "Old", CreatedAt: at("2026-01-01")},
				{ID: 2, Name: "New but closed", CreatedAt: at("2026-08-01"), EndAt: at("2026-08-10")},
			},
			want: 1,
		},
		{
			name: "without dates, the highest id wins",
			courses: []canvas.Course{
				{ID: 164446, Name: "AWS Academy Learner Lab"},
				{ID: 182613, Name: "AWS Academy Learner Lab"},
			},
			want: 182613,
		},
		{
			name:    "only one",
			courses: []canvas.Course{{ID: 42, Name: "The only one"}},
			want:    42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.courses[suggestCourse(tt.courses)]
			if got.ID != tt.want {
				t.Errorf("suggested = %d, expected %d", got.ID, tt.want)
			}
		})
	}
}

func TestCourseLabelDisambiguates(t *testing.T) {
	// Two identically named courses have to produce different labels: otherwise
	// the user has no way to choose.
	a := canvas.Course{ID: 164446, Name: "AWS Academy Learner Lab", CreatedAt: at("2026-03-01")}
	b := canvas.Course{ID: 182613, Name: "AWS Academy Learner Lab", CreatedAt: at("2026-08-01")}

	if a.Label() == b.Label() {
		t.Fatalf("both labels are %q; they cannot be told apart", a.Label())
	}
	if !strings.Contains(a.Label(), "2026-03-01") {
		t.Errorf("the label should include the date: %q", a.Label())
	}
}

func TestCourseEnded(t *testing.T) {
	past := canvas.Course{EndAt: at("2020-01-01")}
	if !past.Ended() {
		t.Error("a course with a past end date should count as ended")
	}
	open := canvas.Course{EndAt: at("2099-01-01")}
	if open.Ended() {
		t.Error("a course with a future end date is not ended")
	}
	unknown := canvas.Course{}
	if unknown.Ended() {
		t.Error("without dates we cannot claim it ended")
	}
}
