package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout collects what a function prints, since the printers write
// straight to stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = previous }()

	fn()
	w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestPrintBudget(t *testing.T) {
	tests := []struct {
		name        string
		used, total float64
		want        []string
		absent      []string
	}{
		{
			name:  "draws the gauge with the figures beside it",
			used:  18.93,
			total: 50,
			want:  []string{"████████░░░░░░░░░░░░", " 38%", "$18.93 of $50.00"},
		},
		{
			name:   "an untouched budget is drawn empty",
			used:   0,
			total:  100,
			want:   []string{"░░░░░░░░░░░░░░░░░░░░", "  0%", "$0.00 of $100.00"},
			absent: []string{"█"},
		},
		{
			name:  "an almost exhausted budget warns and says what is left",
			used:  46.12,
			total: 50,
			want:  []string{" 92%", "$3.88 left", "the lab stops"},
		},
		{
			name:   "with no cap only the spend is printed",
			used:   4.5,
			total:  0,
			want:   []string{"$4.50 used"},
			absent: []string{"█", "░", "%"},
		},
		{
			// Nothing is known: printing "$0.00 of $0.00" would be inventing a
			// budget that was never read.
			name:   "nothing is printed when nothing was read",
			used:   0,
			total:  0,
			absent: []string{"budget"},
		},
		{
			// Vocareum reports the spend a while after it happens, so it can
			// land above the cap. The bar has to stay a bar.
			name:  "an overspent budget stays inside its width",
			used:  60,
			total: 50,
			want:  []string{"████████████████████", "120%", "no budget left"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() { printBudget("    ", tt.used, tt.total) })
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("expected %q in the output, got:\n%s", want, out)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(out, absent) {
					t.Errorf("did not expect %q in the output, got:\n%s", absent, out)
				}
			}
		})
	}
}
