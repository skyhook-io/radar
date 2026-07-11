package main

import "github.com/skyhook-io/radar/internal/contextname"

func clusterShortName(contextName string) string {
	return contextname.ShortName(contextName)
}
