package k8s

import (
	"fmt"
	"strings"
)

const duplicateEnvVarFingerprintPrefix = "dup-env"

type DuplicateEnvVarFingerprint struct {
	Namespace    string
	WorkloadName string
	Container    string
	EnvName      string
}

func FormatDuplicateEnvVarFingerprint(namespace, workloadName, container, envName string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s", duplicateEnvVarFingerprintPrefix, namespace, workloadName, container, envName)
}

func ParseDuplicateEnvVarFingerprint(fingerprint string) (DuplicateEnvVarFingerprint, bool) {
	parts := strings.SplitN(fingerprint, ":", 5)
	if len(parts) != 5 || parts[0] != duplicateEnvVarFingerprintPrefix {
		return DuplicateEnvVarFingerprint{}, false
	}
	return DuplicateEnvVarFingerprint{
		Namespace:    parts[1],
		WorkloadName: parts[2],
		Container:    parts[3],
		EnvName:      parts[4],
	}, true
}
