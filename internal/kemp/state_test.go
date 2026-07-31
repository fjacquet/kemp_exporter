package kemp

import "testing"

func TestUpSample(t *testing.T) {
	up := upSample("lm-01", true)
	if up.Name != "kemp_up" || up.Value != 1 {
		t.Errorf("upSample(true) = %+v, want kemp_up = 1", up)
	}
	if len(up.Labels) != 1 || up.Labels[0].Key != "system" || up.Labels[0].Value != "lm-01" {
		t.Errorf("labels = %+v, want {system=lm-01}", up.Labels)
	}

	// The false branch must carry the same Name and Labels as the true branch —
	// only Value should differ. A prior draft of this test only checked
	// down.Value, which would still pass an implementation that dropped Name
	// and Labels entirely for the down case (e.g. returning a bare
	// Sample{Value: 0}). Assert the full struct to close that gap.
	down := upSample("lm-01", false)
	if down.Name != "kemp_up" || down.Value != 0 {
		t.Errorf("upSample(false) = %+v, want kemp_up = 0", down)
	}
	if len(down.Labels) != 1 || down.Labels[0].Key != "system" || down.Labels[0].Value != "lm-01" {
		t.Errorf("labels = %+v, want {system=lm-01}", down.Labels)
	}
}
