package server

import corev1 "k8s.io/api/core/v1"

type controllerPodHealth struct {
	Ready       int
	Total       int
	Crashing    int
	Pending     int
	CrashReason string
}

func summarizeControllerPods(pods []*corev1.Pod) controllerPodHealth {
	out := controllerPodHealth{Total: len(pods)}
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil && (cs.State.Waiting.Reason == "CrashLoopBackOff" || cs.State.Waiting.Reason == "Error") {
				out.Crashing++
				if out.CrashReason == "" {
					out.CrashReason = cs.State.Waiting.Reason
				}
				break
			}
		}
		if isPodReady(p) {
			out.Ready++
		}
		if p.Status.Phase == corev1.PodPending {
			out.Pending++
		}
	}
	if out.Ready > out.Total {
		out.Ready = out.Total
	}
	return out
}
