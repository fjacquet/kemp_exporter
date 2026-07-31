package kemp

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// DumpSamples writes every sample in exposition format, sorted, one per line.
//
// This is the live-validation tool: run `--once --debug` against a real appliance
// and diff the output against docs/metrics.md. It catches silently-absent metrics
// that kemp_up cannot — a collector reporting OK does not mean every field parsed.
//
// Returns the first write error encountered, if any, so a broken output pipe (e.g.
// stdout closed by a truncating consumer) is reported rather than swallowed.
func DumpSamples(w io.Writer, snap *Snapshot) error {
	var lines []string
	for _, sys := range snap.Systems {
		for _, s := range sys.Samples {
			lines = append(lines, formatSample(s))
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		if _, err := fmt.Fprintln(w, l); err != nil {
			return err
		}
	}
	return nil
}

// formatSample renders one sample as name{k="v",...} value, preserving label order.
func formatSample(s Sample) string {
	var sb strings.Builder
	sb.WriteString(s.Name)
	if len(s.Labels) > 0 {
		sb.WriteByte('{')
		for i, l := range s.Labels {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(l.Key)
			sb.WriteString(`="`)
			sb.WriteString(l.Value)
			sb.WriteByte('"')
		}
		sb.WriteByte('}')
	}
	sb.WriteByte(' ')
	sb.WriteString(strconv.FormatFloat(s.Value, 'g', -1, 64))
	return sb.String()
}
