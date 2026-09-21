package k8score

import corev1 "k8s.io/api/core/v1"

func ResolveServiceTargetPort(port corev1.ServicePort, containers []corev1.Container) (int, bool) {
	if port.TargetPort.IntVal > 0 {
		return int(port.TargetPort.IntVal), true
	}
	if port.TargetPort.StrVal == "" {
		return int(port.Port), true
	}
	for _, container := range containers {
		for _, containerPort := range container.Ports {
			if containerPort.Name == port.TargetPort.StrVal {
				return int(containerPort.ContainerPort), true
			}
		}
	}
	return 0, false
}
