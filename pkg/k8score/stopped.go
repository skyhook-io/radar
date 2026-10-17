package k8score

// Stopped reports whether Stop has been called. A stopped cache's stores keep
// what they held and still report synced, but no longer follow the cluster.
func (rc *ResourceCache) Stopped() bool {
	if rc == nil {
		return false
	}
	select {
	case <-rc.stopCh:
		return true
	default:
		return false
	}
}

// Stopped reports whether Stop has been called; see ResourceCache.Stopped.
func (d *DynamicResourceCache) Stopped() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.stopped
}
