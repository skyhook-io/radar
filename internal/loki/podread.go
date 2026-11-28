package loki

import (
	"context"
	"errors"
)

// ReadPod reads one pod's lines within one read slot. It first asks Loki
// which streams the pod has in w, without a container, and decides container
// support from those streams alone, not from labels other workloads carry:
// a chosen container is matched in Loki only when every stream carries the
// same container label; otherwise the pod is read whole and only the chosen
// container's lines and whole-pod lines are kept (Truncated still describes
// the whole read). Each line's container comes from the alias its stream
// carries.
func ReadPod(ctx context.Context, l StreamLabels, namespace, pod, container string, w Window, limit int) (Lines, error) {
	if reason := l.HistoryReason(); reason != "" {
		return Lines{}, errors.New(reason)
	}
	release, err := acquireRead(readerKey(ctx))
	if err != nil {
		return Lines{}, err
	}
	defer release()

	base := l
	base.Container = ""
	whole, err := PodSelector(base, namespace, pod, "")
	if err != nil {
		return Lines{}, err
	}
	series, err := seriesIn(ctx, []string{whole}, w)
	if err != nil {
		return Lines{}, err
	}
	aliases := map[string]bool{}
	unlabelled := false
	for _, s := range series {
		if alias := containerAlias(s); alias != "" {
			aliases[alias] = true
		} else {
			unlabelled = true
		}
	}
	matched := !unlabelled && len(aliases) == 1
	if matched {
		for alias := range aliases {
			base.Container = alias
		}
	}
	selector, err := PodSelector(base, namespace, pod, container)
	if err != nil {
		return Lines{}, err
	}
	containerOf := func(stream map[string]string) string {
		if alias := containerAlias(stream); alias != "" {
			return stream[alias]
		}
		return ""
	}
	lines, err := queryRange(ctx, l, containerOf, selector, w, limit)
	if err != nil {
		return Lines{}, err
	}
	if container != "" && !matched {
		kept := lines.Items[:0]
		for _, line := range lines.Items {
			if line.Container == container || line.Container == "" {
				kept = append(kept, line)
			}
		}
		lines.Items = kept
	}
	return lines, nil
}
